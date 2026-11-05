// SPDX-License-Identifier: AGPL-3.0-or-later

// Package importer adopts existing deployments into the fleet repository
// without touching them: the first apply after an import must not recreate
// a single container.
package importer

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/secrets"
)

// Project is a Compose project found on this host.
type Project struct {
	Name        string
	Status      string
	ConfigFiles []string
	WorkingDir  string
	EnvFiles    []string
}

// ListCompose lists the Compose projects the local daemon knows, running or not.
func ListCompose(ctx context.Context, r deploy.Runner) ([]Project, error) {
	out, err := r.Run(ctx, "", "compose", "ls", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name, Status, ConfigFiles string
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("docker compose ls: %w", err)
	}
	var projects []Project
	for _, p := range raw {
		proj := Project{Name: p.Name, Status: p.Status, ConfigFiles: splitList(p.ConfigFiles)}
		labels, err := r.Run(ctx, "", "ps", "--all", "--filter", "label=com.docker.compose.project="+p.Name, "--format",
			`{{.Label "com.docker.compose.project.working_dir"}}|{{.Label "com.docker.compose.project.environment_file"}}`)
		if err == nil {
			first, _, _ := strings.Cut(strings.TrimSpace(string(labels)), "\n")
			wd, envs, _ := strings.Cut(first, "|")
			proj.WorkingDir, proj.EnvFiles = wd, splitList(envs)
		}
		if proj.WorkingDir == "" && len(proj.ConfigFiles) > 0 {
			proj.WorkingDir = filepath.Dir(proj.ConfigFiles[0])
		}
		if len(proj.EnvFiles) == 0 && proj.WorkingDir != "" {
			if _, err := os.Stat(filepath.Join(proj.WorkingDir, ".env")); err == nil {
				proj.EnvFiles = []string{filepath.Join(proj.WorkingDir, ".env")}
			}
		}
		projects = append(projects, proj)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Result describes one import.
type Result struct {
	Stack   string
	Dir     string
	Values  []string // variable names stored in stack.yaml
	Secrets []string // variable names stored encrypted
	// Volumes are the named volumes the stack keeps using ("name (external)").
	Volumes  []string
	Warnings []string
}

// volumes lists top-level named volumes with their real Docker names.
func volumes(cfg []byte) []string {
	var doc struct {
		Volumes map[string]struct {
			Name     string `yaml:"name"`
			External bool   `yaml:"external"`
		} `yaml:"volumes"`
	}
	if yaml.Unmarshal(cfg, &doc) != nil {
		return nil
	}
	var out []string
	for key, v := range doc.Volumes {
		name := v.Name
		if name == "" {
			name = key
		}
		if v.External {
			name += " (external)"
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// secretName guesses which variables hold secrets. It errs on the side of
// encrypting: a value encrypted needlessly costs nothing.
var secretName = regexp.MustCompile(`(?i)(PASS|PW|SECRET|TOKEN|KEY|PRIVATE|CREDENTIAL|AUTH|SALT|HASH|CERT|DSN|JWT|SESSION|COOKIE|PIN|SIGN|API|_URI$|_URL$)`)

// stackName mirrors fleet's rule for stack directory names.
var stackName = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?$`)

// Options tune an import.
type Options struct {
	Target string
	// Mode is the stack mode (default compose).
	Mode string
	// Source names where the project came from, for the file header
	// (default: its config files).
	Source string
	// RewriteDir replaces a temporary project directory in the normalised
	// compose file (from → to), e.g. with where Portainer keeps the stack.
	RewriteDir [2]string
	// EncryptAll stores every env value encrypted, not only secret-looking ones.
	EncryptAll bool
}

// ImportCompose writes stacks/<project>/ from a running Compose project.
func ImportCompose(ctx context.Context, r deploy.Runner, f *fleet.Fleet, store secrets.Store, p Project, o Options) (res *Result, err error) {
	if len(p.ConfigFiles) == 0 {
		return nil, fmt.Errorf("%s: compose did not report its config files", p.Name)
	}
	if !stackName.MatchString(p.Name) {
		return nil, fmt.Errorf("%q cannot be a stack name (lowercase letters, digits, - and _, at most 63, not ending in - or _)", p.Name)
	}
	dir := filepath.Join(f.Dir, "stacks", p.Name)
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("%s already exists", dir)
	}
	res = &Result{Stack: p.Name, Dir: dir}
	// Never leave a half-written stack behind: a retry would refuse it.
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()

	// Normalised, but not interpolated: relative paths become absolute (so
	// bind mounts keep pointing at the existing data) and ${VAR} stays.
	args := []string{"compose", "--project-name", p.Name}
	if p.WorkingDir != "" {
		args = append(args, "--project-directory", p.WorkingDir)
	}
	for _, cf := range p.ConfigFiles {
		args = append(args, "--file", cf)
	}
	for _, ef := range p.EnvFiles {
		args = append(args, "--env-file", ef)
	}
	cfg, err := r.Run(ctx, p.WorkingDir, append(args, "config", "--no-interpolate")...)
	if err != nil {
		return nil, err
	}

	env := map[string]string{}
	for _, ef := range p.EnvFiles {
		vars, fromShell, err := parseEnvFile(ef, env)
		if err != nil {
			return nil, err
		}
		for k, v := range vars {
			env[k] = v
		}
		for _, name := range fromShell {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s refers to $%s, which is not defined in the env files (the shell may have provided it); check the imported value", ef, name))
		}
	}
	res.Warnings = append(res.Warnings, inspect(cfg, env)...)

	// Secrets written literally into the compose file would otherwise land in
	// git in plaintext: move them to the encrypted values, referenced as
	// ${NAME}. The container gets the same value, so nothing is recreated.
	cfg, moved, moveWarnings, err := extractLiteralSecrets(cfg, env, o.EncryptAll)
	if err != nil {
		return nil, err
	}
	res.Warnings = append(res.Warnings, moveWarnings...)
	for name, m := range moved {
		env[name] = m.value
		res.Warnings = append(res.Warnings, fmt.Sprintf("service %s had %s written into the compose file; it is now stored encrypted as ${%s}", m.service, m.key, name))
	}

	values, sec := map[string]string{}, map[string]string{}
	for k, v := range env {
		if strings.ContainsAny(v, "'\n\r") {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s contains a single quote or newline and cannot be written to an env file literally; change it before applying", k))
		}
		if o.EncryptAll || secretName.MatchString(k) {
			sec[k] = v
			res.Secrets = append(res.Secrets, k)
		} else {
			values[k] = v
			res.Values = append(res.Values, k)
		}
	}
	sort.Strings(res.Values)
	sort.Strings(res.Secrets)

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	if from, to := o.RewriteDir[0], o.RewriteDir[1]; from != "" {
		cfg = []byte(strings.ReplaceAll(string(cfg), from, to))
	}
	source := o.Source
	if source == "" {
		source = strings.Join(p.ConfigFiles, ", ")
	}
	header := fmt.Sprintf("# Imported by NepFleet on %s from %s\n", time.Now().UTC().Format("2006-01-02"), source)
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), append([]byte(header), cfg...), 0o600); err != nil {
		return nil, err
	}
	stack := map[string]any{
		"apiVersion": "nepfleet/v1alpha1",
		"kind":       "Stack",
		"metadata":   map[string]string{"name": p.Name},
		"spec": map[string]any{
			"target": o.Target,
			"mode":   cmp.Or(o.Mode, "compose"),
			"apply":  "manual",
		},
	}
	if len(values) > 0 {
		stack["spec"].(map[string]any)["values"] = values
	}
	raw, err := yaml.Marshal(stack)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), append([]byte(fleet.Modeline("stack.schema.json")), raw...), 0o600); err != nil {
		return nil, err
	}
	if len(sec) > 0 {
		if err := store.Save(filepath.Join(dir, fleet.SecretsFile), sec); err != nil {
			return nil, fmt.Errorf("encrypting secrets (nothing was imported): %w", err)
		}
	}
	res.Volumes = volumes(cfg)
	return res, nil
}

type movedSecret struct{ service, key, value string }

// extractLiteralSecrets rewrites services.*.environment entries whose name
// looks secret (or all, with all) and whose value is a literal into ${NAME}
// references. NAME is the key itself unless the env files already define it
// differently; then <SERVICE>_<KEY>.
var varName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func extractLiteralSecrets(cfg []byte, env map[string]string, all bool) ([]byte, map[string]movedSecret, []string, error) {
	var warnings []string
	var doc yaml.Node
	if err := yaml.Unmarshal(cfg, &doc); err != nil {
		return nil, nil, nil, err
	}
	moved := map[string]movedSecret{}
	root := doc.Content[0]
	services := child(root, "services")
	for i := 0; services != nil && i+1 < len(services.Content); i += 2 {
		svc := services.Content[i].Value
		envNode := child(services.Content[i+1], "environment")
		if envNode == nil || envNode.Kind != yaml.MappingNode {
			continue // normalised compose always uses the mapping form
		}
		for j := 0; j+1 < len(envNode.Content); j += 2 {
			key, val := envNode.Content[j].Value, envNode.Content[j+1]
			if val.Kind != yaml.ScalarNode || val.Tag == "!!null" || strings.Contains(val.Value, "$") || val.Value == "" {
				continue
			}
			if !all && !secretName.MatchString(key) {
				continue
			}
			// Only keys that are valid variable names can become ${NAME};
			// others (spring.datasource.password) stay and are reported.
			if !varName.MatchString(key) {
				warnings = append(warnings, fmt.Sprintf("service %s has %s written into the compose file; it cannot be moved to secrets automatically, so it is stored in plaintext. Move it by hand", svc, key))
				continue
			}
			taken := func(n string) bool {
				if have, ok := env[n]; ok && have != val.Value {
					return true
				}
				m, ok := moved[n]
				return ok && m.value != val.Value
			}
			name := key
			if taken(name) {
				name = strings.ToUpper(regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(svc, "_")) + "_" + key
			}
			for i := 2; taken(name); i++ {
				name = fmt.Sprintf("%s_%d", strings.TrimRight(name, "_0123456789"), i)
			}
			moved[name] = movedSecret{service: svc, key: key, value: val.Value}
			val.Value, val.Style = "${"+name+"}", 0
		}
	}
	if len(moved) == 0 {
		return cfg, nil, warnings, nil
	}
	out, err := yaml.Marshal(&doc)
	return out, moved, warnings, err
}

func child(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)([:?+-][^}]*)?\}`)

// inspect warns about things an import cannot carry into the fleet repository.
func inspect(cfg []byte, env map[string]string) []string {
	var warn []string
	var doc struct {
		Services map[string]struct {
			Build   any `yaml:"build"`
			EnvFile []struct {
				Path string `yaml:"path"`
			} `yaml:"env_file"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(cfg, &doc); err == nil {
		names := make([]string, 0, len(doc.Services))
		for n := range doc.Services {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			s := doc.Services[n]
			if s.Build != nil {
				warn = append(warn, fmt.Sprintf("service %s is built locally; the build context stays outside the fleet repository", n))
			}
			for _, ef := range s.EnvFile {
				warn = append(warn, fmt.Sprintf("service %s reads %s, which is not imported (secrets in it stay unencrypted on disk)", n, ef.Path))
			}
		}
	}
	seen := map[string]bool{}
	for _, m := range varRef.FindAllSubmatch(cfg, -1) {
		name, hasDefault := string(m[1]), len(m[2]) > 0
		if _, ok := env[name]; !ok && !hasDefault && !seen[name] {
			seen[name] = true
			warn = append(warn, fmt.Sprintf("${%s} has no value in the project's env files (it may have come from the shell); add it to values or secrets", name))
		}
	}
	return warn
}

// parseEnvFile reads an env file the way Compose does: comments, export
// prefix, single quotes (literal), double quotes (escapes \n \" \\ and
// interpolation), unquoted values (interpolation, " #" starts a comment).
// Interpolation uses vars (earlier files and lines); names that are not
// defined there would come from the shell at deploy time and are reported.
func parseEnvFile(path string, vars map[string]string) (map[string]string, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	var fromShell []string
	lookup := func(name string) (string, bool) {
		if v, ok := out[name]; ok {
			return v, true
		}
		v, ok := vars[name]
		return v, ok
	}
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, raw, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, nil, fmt.Errorf("%s:%d: not KEY=VALUE", path, n)
		}
		raw = strings.TrimSpace(raw)
		var v string
		switch {
		case strings.HasPrefix(raw, "'"):
			end := strings.Index(raw[1:], "'")
			if end < 0 {
				return nil, nil, fmt.Errorf("%s:%d: unterminated single quote", path, n)
			}
			v = raw[1 : end+1]
		case strings.HasPrefix(raw, `"`):
			var b strings.Builder
			i := 1
			for ; i < len(raw) && raw[i] != '"'; i++ {
				if raw[i] == '\\' && i+1 < len(raw) {
					i++
					switch raw[i] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					default:
						b.WriteByte(raw[i])
					}
					continue
				}
				b.WriteByte(raw[i])
			}
			if i >= len(raw) {
				return nil, nil, fmt.Errorf("%s:%d: unterminated double quote", path, n)
			}
			var missing []string
			v, missing = interpolate(b.String(), lookup)
			fromShell = append(fromShell, missing...)
		default:
			if i := strings.Index(raw, " #"); i >= 0 {
				raw = strings.TrimSpace(raw[:i])
			}
			var missing []string
			v, missing = interpolate(raw, lookup)
			fromShell = append(fromShell, missing...)
		}
		out[k] = v
	}
	return out, fromShell, sc.Err()
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?[-?+])([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// interpolate expands ${VAR}, ${VAR:-def}, ${VAR-def}, ${VAR:+alt} and $VAR.
// $$ is a literal $. It returns the names that were not defined.
func interpolate(s string, lookup func(string) (string, bool)) (string, []string) {
	var missing []string
	s = strings.ReplaceAll(s, "$$", "\x00")
	s = envRef.ReplaceAllStringFunc(s, func(m string) string {
		g := envRef.FindStringSubmatch(m)
		name, op, arg := g[1], g[2], g[3]
		if name == "" {
			name = g[4]
		}
		v, ok := lookup(name)
		switch op {
		case ":-":
			if !ok || v == "" {
				return arg
			}
		case "-":
			if !ok {
				return arg
			}
		case ":+":
			if ok && v != "" {
				return arg
			}
			return ""
		case "+":
			if ok {
				return arg
			}
			return ""
		}
		if !ok {
			missing = append(missing, name)
		}
		return v
	})
	return strings.ReplaceAll(s, "\x00", "$"), missing
}
