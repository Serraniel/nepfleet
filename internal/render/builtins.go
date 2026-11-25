// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

// IsBuiltin reports a variable NepFleet provides to every stack
// (template.Builtins).
func IsBuiltin(name string) bool {
	return slices.Contains(template.Builtins, name)
}

// mailEnv adds the fleet's mail settings (spec.mail) as
// ${NEPFLEET_SMTP_*}, all empty when the fleet has none. The password is a
// secret: masked in plans and output.
func mailEnv(in Input, env map[string]string, out *Output) {
	var m fleet.Mail
	if in.Fleet.Spec.Mail != nil {
		m = *in.Fleet.Spec.Mail
	}
	port := ""
	if m.Port != 0 {
		port = strconv.Itoa(m.Port)
	}
	env["NEPFLEET_SMTP_HOST"] = m.Host
	env["NEPFLEET_SMTP_PORT"] = port
	env["NEPFLEET_SMTP_USER"] = m.User
	env["NEPFLEET_SMTP_PASSWORD"] = in.MailPassword
	env["NEPFLEET_SMTP_FROM"] = m.From
	env["NEPFLEET_SMTP_TLS"] = m.TLS
	out.Secret["NEPFLEET_SMTP_PASSWORD"] = true
}

// varRef matches what Compose interpolates: ${NAME…} (also with a default or
// an error, ${NAME:-x}) and $NAME. $$ is an escaped dollar, not a reference.
var varRef = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)|\$([A-Za-z_][A-Za-z0-9_]*)`)

// referenced lists the variable names the files refer to.
func referenced(files map[string][]byte) map[string]bool {
	refs := map[string]bool{}
	for _, raw := range files {
		for _, m := range varRef.FindAllSubmatch(raw, -1) {
			switch {
			case len(m[1]) > 0:
				refs[string(m[1])] = true
			case len(m[2]) > 0:
				refs[string(m[2])] = true
			}
		}
	}
	return refs
}

// unusedBuiltins removes from env the built-ins nothing in the rendered files
// refers to, so a built-in NepFleet adds changes only the stacks that ask
// for it, and returns them with their values. builtin names the keys that
// hold a built-in (not a stack value of the same name).
func unusedBuiltins(env map[string]string, builtin map[string]bool, files map[string][]byte) map[string]string {
	use := composeUse(files)
	if use.all {
		return nil
	}
	refs := referenced(files)
	dropped := map[string]string{}
	for k := range builtin {
		if v, ok := env[k]; ok && !refs[k] && !use.passthrough[k] {
			dropped[k] = v
			delete(env, k)
		}
	}
	return dropped
}

// envUse is what compose takes from .env besides ${NAME} references.
type envUse struct {
	// all: a service loads .env itself (env_file: .env), or a compose file
	// can't be read, so every variable may reach a container.
	all bool
	// passthrough: environment entries without a value ([TZ] or {TZ: }),
	// which compose fills from .env.
	passthrough map[string]bool
}

// composeService is the part of a service that takes variables from .env.
type composeService struct {
	EnvFile     any            `yaml:"env_file"`
	Environment any            `yaml:"environment"`
	Extends     map[string]any `yaml:"extends"`
}

// composeUse reads compose.yaml and the rendered compose files it includes
// or extends (include:, extends.file). Files outside the rendered stack (an
// absolute path, ../) can't be seen; their own ${NAME} references can't
// either, so such stacks should not rely on built-ins through them.
func composeUse(files map[string][]byte) envUse {
	use := envUse{passthrough: map[string]bool{}}
	queue := []string{ComposeFile}
	seen := map[string]bool{}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		raw, ok := files[name]
		if !ok {
			continue
		}
		var doc struct {
			Include  []any                     `yaml:"include"`
			Services map[string]composeService `yaml:"services"`
		}
		if yaml.Unmarshal(raw, &doc) != nil {
			use.all = true
			return use
		}
		dir := path.Dir(name)
		rel := func(p string) string { return path.Join(dir, p) }
		for _, inc := range doc.Include {
			for _, p := range paths(inc, "path") {
				queue = append(queue, rel(p))
			}
		}
		for _, s := range doc.Services {
			if slices.ContainsFunc(paths(s.EnvFile, "path"), func(p string) bool { return rel(p) == EnvFile }) {
				use.all = true
				return use
			}
			for _, k := range passthrough(s.Environment) {
				use.passthrough[k] = true
			}
			if f, ok := s.Extends["file"].(string); ok {
				queue = append(queue, rel(f))
			}
		}
	}
	return use
}

// paths reads a compose path entry: a string, a map with key, or a list of
// either (key's value may itself be a list, as include's path).
func paths(v any, key string) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case map[string]any:
		return paths(v[key], key)
	case []any:
		var out []string
		for _, e := range v {
			out = append(out, paths(e, key)...)
		}
		return out
	}
	return nil
}

// passthrough lists environment entries without a value: [NAME] in a list,
// NAME: (null) in a map.
func passthrough(env any) []string {
	var out []string
	switch env := env.(type) {
	case []any:
		for _, e := range env {
			if s, ok := e.(string); ok && !strings.Contains(s, "=") {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case map[string]any:
		for k, v := range env {
			if v == nil {
				out = append(out, k)
			}
		}
	}
	return out
}
