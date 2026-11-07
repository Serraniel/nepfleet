// SPDX-License-Identifier: AGPL-3.0-or-later

// Package render turns a stack (template + values + secrets) into the files
// that are deployed: compose.yaml, .env and the template's extra files.
package render

import (
	"errors"
	"fmt"
	"maps"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

// Input is everything rendering needs.
type Input struct {
	Fleet *fleet.Fleet
	Stack *fleet.Stack
	// Template is nil for a plain compose stack.
	Template *template.Template
	// Secrets are the decrypted values.secret.yaml.
	Secrets map[string]string
	// Refs are the resolved values of references to other stacks
	// (Stack.Spec.Values entries with from), by key.
	Refs map[string]string
	// RefSecret marks references whose value comes from a secret, also
	// through a chain of value references: they are masked.
	RefSecret map[string]bool
	// NoBackupVolumes are the Docker names of volumes that templates of
	// stacks on the same host mark backup: false (caches, models), for
	// ${NEPFLEET_NO_BACKUP_VOLUMES}.
	NoBackupVolumes []string
	// Host is the stack's target host (address for port access).
	Host *fleet.Host
	// StoragePasswords are the passwords of fleet.yaml spec.storage entries
	// (from fleet.secret.yaml), by storage name.
	StoragePasswords map[string]string
	// RenderGID is the group of the host's /dev/dri render nodes, added to
	// services that get an intel or amd GPU, so non-root images can use it.
	RenderGID string
}

// Output is the rendered stack.
type Output struct {
	// Files maps paths relative to the stack's working directory to content.
	Files map[string][]byte
	// Generated holds values created by this render; persist them to the
	// secrets file before deploying, or they change on every render.
	Generated map[string]string
	// Secret names, for masking in plans and logs.
	Secret map[string]bool
	// secretValues are the values of Secret, for MaskSecrets.
	secretValues []string
}

// EnvFile and ComposeFile are the names used in the working directory.
const (
	EnvFile     = ".env"
	ComposeFile = "compose.yaml"
)

// Render produces the stack's files. It never writes to disk.
func Render(in Input) (*Output, error) {
	out := &Output{Files: map[string][]byte{}, Generated: map[string]string{}, Secret: map[string]bool{}}
	env := map[string]string{
		"NEPFLEET_STACK":           in.Stack.Metadata.Name,
		"NEPFLEET_INGRESS_NETWORK": in.Fleet.Spec.IngressNetwork,
		"TZ":                       in.Fleet.Spec.TZ,
		// Backups encrypted to these can be read with the fleet's key.
		"NEPFLEET_AGE_RECIPIENTS":    strings.Join(in.Fleet.Spec.Secrets.Age, ","),
		"NEPFLEET_NO_BACKUP_VOLUMES": noBackupPattern(in.NoBackupVolumes),
	}
	var compose []byte
	var err error

	if in.Template == nil {
		// Plain stack: the compose file is the user's; values and secrets pass through.
		compose, err = os.ReadFile(filepath.Join(in.Stack.Dir, ComposeFile))
		if err != nil {
			return nil, err
		}
		for k, v := range in.Stack.Spec.Values {
			r, err := in.value(k, v, out)
			if err != nil {
				return nil, err
			}
			env[k] = r
		}
		for k, v := range in.Secrets {
			env[k] = v
			out.Secret[k] = true
		}
	} else {
		if portMode(in.Stack) {
			in.Stack = withHostDomain(in)
		}
		if err := values(in, env, out); err != nil {
			return nil, err
		}
		// Before the files are substituted: they may use it too.
		env["NEPFLEET_URL"] = stackURL(in, env)
		compose, err = os.ReadFile(filepath.Join(in.Template.Dir, in.Template.Spec.Compose))
		if err != nil {
			return nil, err
		}
		for _, f := range in.Template.Spec.Files {
			raw, err := os.ReadFile(filepath.Join(in.Template.Dir, f.Src))
			if err != nil {
				return nil, err
			}
			out.Files[f.Dest] = substitute(raw, env)
		}
	}
	if _, ok := out.Files[ComposeFile]; ok {
		return nil, errors.New("a template file may not be called " + ComposeFile)
	}
	if in.Stack.Dir != "" {
		if err := stackFiles(in.Stack.Dir, env, out); err != nil {
			return nil, err
		}
	}
	if in.Template != nil && len(in.Template.Spec.Hardware) > 0 {
		if compose, err = WithHardware(compose, in.Template.Spec.Hardware, env, in.Stack.Spec.Mode, in.RenderGID); err != nil {
			return nil, fmt.Errorf("compose file: %w", err)
		}
	}
	if in.Template == nil {
		env["NEPFLEET_URL"] = stackURL(in, env)
	}
	if compose, err = WithAccess(compose, in.Template, in.Stack); err != nil {
		return nil, fmt.Errorf("compose file: %w", err)
	}
	if compose, err = WithStorage(compose, in.Fleet, in.Stack, in.StoragePasswords, env, out); err != nil {
		return nil, fmt.Errorf("compose file: %w", err)
	}
	if compose, err = WithBlockedHosts(compose, fleet.BlockedHosts(in.Fleet, in.Stack)); err != nil {
		return nil, fmt.Errorf("compose file: %w", err)
	}
	if l := fleet.EffectiveLogging(in.Fleet, in.Stack); l != nil {
		if compose, err = WithLogging(compose, l); err != nil {
			return nil, fmt.Errorf("compose file: %w", err)
		}
	}
	out.Files[ComposeFile] = compose
	envFile, err := dotenv(env)
	if err != nil {
		return nil, err
	}
	out.Files[EnvFile] = envFile
	out.collectSecretValues(env)
	return out, nil
}

// value is a stack value with references resolved; values taken from
// another stack's secrets are masked like this stack's own.
func (in Input) value(key string, v fleet.Value, out *Output) (string, error) {
	if !v.IsRef() {
		return v.Literal, nil
	}
	r, ok := in.Refs[key]
	if !ok {
		return "", fmt.Errorf("%s: %s is not resolved", key, v)
	}
	if v.Secret != "" || in.RefSecret[key] {
		out.Secret[key] = true
	}
	return r, nil
}

// noBackupPattern joins volume names into a regular expression alternation
// ("a_cache|b_models"), names quoted; it never matches when there are none,
// so an exclude built from it cannot exclude everything.
func noBackupPattern(names []string) string {
	if len(names) == 0 {
		return "$^" // matches nothing
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = regexp.QuoteMeta(n)
	}
	sort.Strings(quoted)
	return strings.Join(quoted, "|")
}

// withHostDomain fills DOMAIN with the host's address for a stack on a LAN
// port, so templates that require a domain still render; their web UI is
// reached at ${NEPFLEET_URL}.
func withHostDomain(in Input) *fleet.Stack {
	if in.Template == nil {
		return in.Stack
	}
	if _, set := in.Stack.Spec.Values["DOMAIN"]; set {
		return in.Stack
	}
	for _, inp := range in.Template.Spec.Inputs {
		if inp.Name == "DOMAIN" {
			s := *in.Stack
			s.Spec.Values = maps.Clone(in.Stack.Spec.Values)
			if s.Spec.Values == nil {
				s.Spec.Values = map[string]fleet.Value{}
			}
			addr := s.Spec.Target
			if in.Host != nil && in.Host.Spec.Address != "" {
				addr = in.Host.Spec.Address
			}
			s.Spec.Values["DOMAIN"] = fleet.Lit(addr)
			return &s
		}
	}
	return in.Stack
}

// values validates stack values against the template's inputs and fills env.
func values(in Input, env map[string]string, out *Output) error {
	t := in.Template
	var errs []error
	known := map[string]bool{}
	for _, inp := range t.Spec.Inputs {
		known[inp.Name] = true
		raw, given := in.Stack.Spec.Values[inp.Name]
		v := raw.Literal
		if given && raw.IsRef() {
			var err error
			if v, err = in.value(inp.Name, raw, out); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if inp.Type == "secret" {
			out.Secret[inp.Name] = true
			switch {
			case given && raw.IsRef(): // another stack's value, never stored here
			case given:
				errs = append(errs, fmt.Errorf("%s is a secret: set it with `nepfleet secret set %s %s`, not in stack.yaml",
					inp.Name, in.Stack.Metadata.Name, inp.Name))
				continue
			default:
				v, given = in.Secrets[inp.Name]
			}
		}
		// An empty value means unset for inputs whose type has no empty
		// value (an enum, a number, a host name…): the default applies, or
		// the input is missing. Otherwise "" would skip every check.
		if !given || (v == "" && TypedInput(inp.Type)) {
			v = inp.Default
		}
		if v == "" {
			if inp.Required {
				errs = append(errs, fmt.Errorf("%s is required", inp.Name))
			}
			env[inp.Name] = ""
			continue
		}
		if inp.Type == "path" && strings.HasPrefix(v, "@") {
			p, err := in.Fleet.ResolvePath(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", inp.Name, err))
				continue
			}
			v = p
		}
		if err := CheckInput(inp, v); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", inp.Name, err))
		}
		env[inp.Name] = v
	}
	for _, g := range t.Spec.Generate {
		known[g.Name] = true
		out.Secret[g.Name] = true
		v, ok := in.Secrets[g.Name]
		if !ok {
			var err error
			if v, err = g.Value(); err != nil {
				return err
			}
			out.Generated[g.Name] = v
		}
		env[g.Name] = v
	}
	var unknown []string
	for k := range in.Stack.Spec.Values {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		errs = append(errs, fmt.Errorf("%s is not an input of template %s", k, t.Metadata.Name))
	}
	return errors.Join(errs...)
}

var (
	hostnameRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
)

// TypedInput reports an input type that has no empty value, so
// an empty value means unset (the default applies).
func TypedInput(typ string) bool {
	return slices.Contains([]string{"enum", "bool", "int", "hostname", "url", "email", "gpu"}, typ)
}

// CheckInput validates one value against its input definition. Errors about
// secret inputs never contain the value.
func CheckInput(inp template.Input, v string) error {
	err := check(inp, v)
	if err != nil && inp.Type == "secret" {
		return fmt.Errorf("the value does not have the expected format%s", patternHint(inp))
	}
	return err
}

func patternHint(inp template.Input) string {
	if inp.Pattern != "" {
		return " (" + inp.Pattern + ")"
	}
	return ""
}

func check(inp template.Input, v string) error {
	switch inp.Type {
	case "hostname":
		if len(v) > 253 || !hostnameRe.MatchString(v) {
			return fmt.Errorf("%q is not a host name", v)
		}
	case "url":
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%q is not an http(s) URL", v)
		}
	case "email":
		if a, err := mail.ParseAddress(v); err != nil || a.Address != v {
			return fmt.Errorf("%q is not an e-mail address", v)
		}
	case "int":
		if _, err := strconv.Atoi(v); err != nil {
			return fmt.Errorf("%q is not an integer", v)
		}
	case "bool":
		if v != "true" && v != "false" {
			return fmt.Errorf("%q must be true or false", v)
		}
	case "enum":
		if !slices.Contains(inp.Options, v) {
			return fmt.Errorf("%q must be one of %v", v, inp.Options)
		}
	case "device":
		if !template.ValidDevice(v) {
			return fmt.Errorf("%q is not a device path under /dev (letters, digits and ._+@/- only)", v)
		}
	case "gpu":
		if !slices.Contains([]string{"none", "intel", "amd", "nvidia"}, v) {
			return fmt.Errorf("%q must be none, intel, amd or nvidia", v)
		}
	case "path":
		if name, ok := strings.CutPrefix(v, "@"); ok {
			if !fleet.ValidName(name) {
				return fmt.Errorf("%q is not a path name from fleet.yaml spec.paths", v)
			}
			break
		}
		if !filepath.IsAbs(v) && !strings.HasPrefix(v, "./") {
			return fmt.Errorf("%q must be absolute or start with ./", v)
		}
	}
	if inp.Pattern != "" {
		if ok, _ := regexp.MatchString(inp.Pattern, v); !ok {
			return fmt.Errorf("%q does not match %s", v, inp.Pattern)
		}
	}
	return nil
}

// dotenv writes KEY='value' lines. Single quotes keep every character
// literal in Compose (no $ interpolation), so values that contain a single
// quote or a newline cannot be represented and are rejected.
func dotenv(env map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Rendered by NepFleet. Do not edit: changes are overwritten on apply.\n")
	for _, k := range keys {
		v := env[k]
		if strings.ContainsAny(v, "'\n\r") {
			return nil, fmt.Errorf("%s contains a single quote or newline, which an env file cannot hold literally", k)
		}
		// docker compose reads \' inside single quotes as an escaped quote,
		// so a trailing backslash would swallow the closing quote.
		if strings.HasSuffix(v, `\`) {
			return nil, fmt.Errorf("%s ends with a backslash, which an env file cannot hold literally", k)
		}
		fmt.Fprintf(&b, "%s='%s'\n", k, v)
	}
	return []byte(b.String()), nil
}

// EnvValues reads an env file written by dotenv (KEY='value' lines) back
// into a map. It is the one parser for rendered env files.
func EnvValues(envFile []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(envFile), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

// substitute replaces ${NAME} for known names; $${ becomes a literal ${.
func substitute(raw []byte, env map[string]string) []byte {
	pairs := []string{"$${", "\x00"}
	for k, v := range env {
		pairs = append(pairs, "${"+k+"}", v)
	}
	s := strings.NewReplacer(pairs...).Replace(string(raw))
	return []byte(strings.ReplaceAll(s, "\x00", "${"))
}

// Masked returns the env file with secret values replaced, for display.
func (o *Output) Masked(name string) []byte {
	if name != EnvFile {
		return o.MaskSecrets(o.Files[name])
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(string(o.Files[EnvFile]), "\n") {
		if k, _, ok := strings.Cut(line, "="); ok && o.Secret[k] {
			line = k + "='••••••'\n"
		}
		b.WriteString(line)
	}
	return []byte(b.String())
}
