// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
			p.Steps = append(p.Steps, recreateStep(p.Mode, name))
		}
	}
	if p.Mode == "swarm" {
		p.Steps = append(p.Steps, Step{Kind: "health", Text: "docker stack deploy; swarm updates as each service's update_config says and rolls back on failure (volume data is not rolled back)"})
	} else {
		p.Steps = append(p.Steps, Step{Kind: "health", Text: "wait until every service is healthy; on failure restore the previous files and images and apply them again (volume data is not rolled back)"})
	}
	p.Approximate = true // until RefineServices asked compose
	p.Steps = append(p.Steps, Step{Kind: "record", Text: "record this revision as applied"})
}

func recreateStep(mode, name string) Step {
	if mode == "swarm" {
		return Step{Kind: "service", Service: name, Text: "update " + name + " (rolling or stop-first, as its update_config says)"}
	}
	return Step{Kind: "service", Service: name, Text: "recreate " + name + " (it is down while it restarts)"}
}

// RefineServices replaces the per-service steps with compose's own
// decision: the old and the new version are rendered into the same
// private directory and `docker compose config --hash` is compared per
// service, which covers every interpolation form, env_file contents and
// anything else compose hashes. Without docker the approximation stays.
func (p *Plan) RefineServices(ctx context.Context, r Runner, out *render.Output) error {
	if p.Empty() {
		return nil
	}
	tmp, err := os.MkdirTemp("", "nepfleet-plan-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }() // 0700: rendered env files hold secrets
	oldHashes := map[string]string{}
	if !p.First {
		old := map[string][]byte{}
		if man, err := readManifest(p.Dir); err == nil && man != nil {
			for _, f := range man.Files {
				if b, err := os.ReadFile(filepath.Join(p.Dir, f)); err == nil {
					old[f] = b
				}
			}
		}
		if oldHashes, err = hashes(ctx, r, p.Stack, tmp, old); err != nil {
			return err
		}
		if err := os.RemoveAll(tmp); err != nil {
			return err
		}
		if err := os.Mkdir(tmp, 0o700); err != nil {
			return err
		}
	}
	newHashes, err := hashes(ctx, r, p.Stack, tmp, out.Files)
	if err != nil {
		return err
	}
	pinned := map[string]bool{}
	for _, c := range p.Changes {
		if img, ok := strings.CutPrefix(c.Path, "image "); ok {
			pinned[img] = true
		}
	}
	newSvc := services(out.Files[render.ComposeFile], out.Files[render.EnvFile])
	var steps []Step
	inserted := false
	for _, s := range p.Steps {
		if s.Kind == "service" {
			continue
		}
		if s.Kind == "health" && !inserted {
			inserted = true
			for _, name := range sortedKeys(unionS(oldHashes, newHashes)) {
				o, wasThere := oldHashes[name]
				n, isThere := newHashes[name]
				switch {
				case !wasThere:
					steps = append(steps, Step{Kind: "service", Service: name, Text: "create " + name})
				case !isThere:
					steps = append(steps, Step{Kind: "service", Service: name, Text: "remove " + name, Destructive: true})
				case o != n || pinned[image(newSvc[name])]:
					steps = append(steps, recreateStep(p.Mode, name))
				}
			}
		}
		steps = append(steps, s)
	}
	p.Steps, p.Approximate = steps, false
	return nil
}

func hashes(ctx context.Context, r Runner, stack, dir string, files map[string][]byte) (map[string]string, error) {
	out := map[string]string{}
	if len(files[render.ComposeFile]) == 0 {
		return out, nil
	}
	if err := writeFiles(dir, files, nil); err != nil {
		return nil, err
	}
	raw, err := r.Run(ctx, dir, compose(stack, dir, "config", "--hash", "*")...)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if name, h, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			out[name] = h
		}
	}
	return out, nil
}

func unionS(a, b map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// services parses a compose file with its env file applied, so services
// count as changed when their effective configuration differs.
func services(compose, env []byte) map[string]any {
	if len(compose) == 0 {
		return map[string]any{}
	}
	expanded := interpolate(string(compose), render.EnvValues(env))
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

// UseSnapshots records in the steps that the apply copies the running
// stack's volumes first and restores them on rollback.
func (p *Plan) UseSnapshots() {
	var steps []Step
	inserted := false
	for _, st := range p.Steps {
		if !inserted && st.Kind == "service" {
			steps = append(steps, Step{Kind: "snapshot", Text: "stop the running services and copy their volumes (data-safe rollback)"})
			inserted = true
		}
		if st.Kind == "health" && p.Mode != "swarm" {
			st.Text = "wait until every service is healthy; on failure restore the previous files, images and volume data and start the previous version"
		}
		steps = append(steps, st)
	}
	p.Steps = steps
}

// Recreates reports whether the plan creates, recreates or removes services.
func (p *Plan) Recreates() bool {
	for _, st := range p.Steps {
		if st.Kind == "service" {
			return true
		}
	}
	return false
}
