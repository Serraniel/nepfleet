// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"path"
	"regexp"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/template"
)

// IsBuiltin reports a variable NepFleet provides to every stack
// (template.Builtins).
func IsBuiltin(name string) bool {
	return slices.Contains(template.Builtins, name)
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
// for it. builtin names the keys that hold a built-in (not a stack value of
// the same name). A compose file that passes .env itself to containers
// (env_file: .env) keeps them all: they reach the containers.
func unusedBuiltins(env map[string]string, builtin map[string]bool, files map[string][]byte) {
	if loadsEnvFile(files[ComposeFile]) {
		return
	}
	refs := referenced(files)
	for k := range builtin {
		if !refs[k] {
			delete(env, k)
		}
	}
}

// loadsEnvFile reports a service with env_file: .env.
func loadsEnvFile(compose []byte) bool {
	var doc struct {
		Services map[string]struct {
			EnvFile any `yaml:"env_file"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal(compose, &doc) != nil {
		return true // can't tell: keep everything
	}
	isEnv := func(p any) bool {
		switch v := p.(type) {
		case string:
			return path.Clean(v) == EnvFile
		case map[string]any:
			s, _ := v["path"].(string)
			return path.Clean(s) == EnvFile
		}
		return false
	}
	for _, s := range doc.Services {
		switch v := s.EnvFile.(type) {
		case string:
			if isEnv(v) {
				return true
			}
		case []any:
			if slices.ContainsFunc(v, isEnv) {
				return true
			}
		}
	}
	return false
}
