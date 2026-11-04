// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/render"
)

// Step is one action apply will take, in order. The UI's change review and
// the CLI plan render the same list.
type Step struct {
	Kind        string `json:"kind"` // files, secrets, network, maintenance, image, service, health, record
	Text        string `json:"text"`
	Service     string `json:"service,omitempty"`
	Destructive bool   `json:"destructive,omitempty"`
}

// addSteps derives the ordered steps from what PlanStack found. oldDir holds
// the files of the last apply (nil maps for a first deployment).
func (p *Plan) addSteps(oldCompose, oldEnv []byte, out *render.Output, pinChanges map[string]bool) {
	if p.Empty() {
		return
	}
	var files []string
	for _, c := range p.Changes {
		if !strings.HasPrefix(c.Path, "image ") {
			files = append(files, string(c.Kind)+" "+c.Path)
		}
	}
	if len(files) > 0 {
		note := " (previous version kept for rollback)"
		if p.First {
			note = " (first deployment)"
		}
		p.Steps = append(p.Steps, Step{Kind: "files", Text: "write " + strings.Join(files, ", ") + note})
	}
	if len(p.Generated) > 0 {
		p.Steps = append(p.Steps, Step{Kind: "secrets", Text: "generate " + strings.Join(p.Generated, ", ") + " and store them encrypted"})
	}
	newSvc := services(out.Files[render.ComposeFile], out.Files[render.EnvFile])
	oldSvc := services(oldCompose, oldEnv)
	for _, img := range sortedKeys(pinChanges) {
		p.Steps = append(p.Steps, Step{Kind: "image", Text: "pull and pin " + img})
	}
	for _, name := range sortedKeys(union(newSvc, oldSvc)) {
		o, wasThere := oldSvc[name]
		n, isThere := newSvc[name]
		switch {
		case !wasThere:
			p.Steps = append(p.Steps, Step{Kind: "service", Service: name, Text: "create " + name})
		case !isThere:
			p.Steps = append(p.Steps, Step{Kind: "service", Service: name, Text: "remove " + name, Destructive: true})
		case !reflect.DeepEqual(o, n) || pinChanges[image(n)]:
			p.Steps = append(p.Steps, Step{Kind: "service", Service: name, Text: "recreate " + name + " (short downtime unless it runs more than one replica)"})
		}
	}
	if p.Mode == "swarm" {
		p.Steps = append(p.Steps, Step{Kind: "health", Text: "docker stack deploy; swarm rolls services one task at a time and rolls back on failure"})
	} else {
		p.Steps = append(p.Steps, Step{Kind: "health", Text: "wait until every service is healthy; on failure restore the previous files and images and apply them again"})
	}
	p.Steps = append(p.Steps, Step{Kind: "record", Text: "record this revision as applied"})
}

// services parses a compose file with its env file applied, so services
// count as changed when their effective configuration differs.
func services(compose, env []byte) map[string]any {
	if len(compose) == 0 {
		return map[string]any{}
	}
	vars := map[string]string{}
	for _, line := range strings.Split(string(env), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(k, "#") {
			vars[k] = strings.TrimSuffix(strings.TrimPrefix(v, "'"), "'")
		}
	}
	expanded := interpolate(string(compose), vars)
	var doc struct {
		Services map[string]any `yaml:"services"`
	}
	if yaml.Unmarshal([]byte(expanded), &doc) != nil || doc.Services == nil {
		return map[string]any{}
	}
	return doc.Services
}

var refRe = regexp.MustCompile(`\$\$|\$\{([A-Za-z_][A-Za-z0-9_]*)(?::?-([^}]*))?\}`)

func interpolate(s string, vars map[string]string) string {
	return refRe.ReplaceAllStringFunc(s, func(m string) string {
		if m == "$$" {
			return m
		}
		g := refRe.FindStringSubmatch(m)
		if v, ok := vars[g[1]]; ok && v != "" {
			return v
		}
		return g[2]
	})
}

func image(svc any) string {
	if m, ok := svc.(map[string]any); ok {
		return fmt.Sprint(m["image"])
	}
	return ""
}

func union(a, b map[string]any) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
