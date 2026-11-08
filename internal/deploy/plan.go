// SPDX-License-Identifier: AGPL-3.0-or-later

// Package deploy plans and applies rendered stacks on the local Docker host.
//
// Every stack has a working directory under the state root. It holds the
// rendered files, the stack's relative bind mounts (./config ...), and
// .nepfleet/ with the manifest of managed files and the previous version for
// fast rollback.
package deploy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/render"
)

// Kind of a file change.
type Kind string

// Change kinds.
const (
	Added   Kind = "added"
	Changed Kind = "changed"
	Removed Kind = "removed"
)

// Change is one managed file that differs.
type Change struct {
	Path string `json:"path"`
	Kind Kind   `json:"kind"`
	// Diff is a line diff with secrets masked.
	Diff string `json:"diff,omitempty"`
}

// Plan is what applying a stack would change.
type Plan struct {
	Stack       string   `json:"stack"`
	Dir         string   `json:"dir"`
	Mode        string   `json:"mode"`
	First       bool     `json:"first"` // never applied before
	Changes     []Change `json:"changes"`
	Destructive []string `json:"destructive,omitempty"`
	// Migration is the data migration this apply runs, and MigrationError why
	// this apply is refused instead.
	Migration      *PlannedMigration `json:"migration,omitempty"`
	MigrationError string            `json:"migrationError,omitempty"`
	Generated      []string          `json:"generated,omitempty"` // secrets created by this apply
	// Steps is everything apply will do, in order (see steps.go).
	Steps []Step `json:"steps,omitempty"`
	// Approximate means the per-service steps were derived without asking
	// compose (no docker available); compose may recreate more.
	Approximate bool `json:"approximate,omitempty"`
	// Template is the template source commit this plan renders from, and
	// TemplateFrom the pinned one it moves away from (--update-lock).
	Template     string `json:"template,omitempty"`
	TemplateFrom string `json:"templateFrom,omitempty"`
	// Egress is the stack's egress mode when it isn't off (log or enforce), so a
	// review shows that its containers' outbound traffic is filtered or recorded.
	Egress string `json:"egress,omitempty"`
}

// Empty reports whether nothing would change.
func (p *Plan) Empty() bool {
	return len(p.Changes) == 0 && p.Migration == nil && p.MigrationError == ""
}

// Manifest is .nepfleet/manifest.json.
type Manifest struct {
	Files  []string `json:"files"`
	Commit string   `json:"commit,omitempty"`
	Dirty  bool     `json:"dirty,omitempty"`
	// Images are the digests that ran; a rollback re-pins them.
	Images map[string]string `json:"images,omitempty"`
	// Template and TemplateVersion are what was applied: migrations start here.
	Template        string `json:"template,omitempty"`
	TemplateVersion string `json:"templateVersion,omitempty"`
}

const metaDir = ".nepfleet"

// StackDir returns the working directory of a stack.
func StackDir(stateRoot, stack string) string {
	return filepath.Join(stateRoot, "stacks", stack)
}

func readManifest(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, metaDir, "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var m Manifest
	return &m, json.Unmarshal(raw, &m)
}

// PlanStack compares rendered output with the stack's working directory.
// pins (lock.yaml) are compared with the digests of the last apply; nil
// skips that comparison.
func PlanStack(name, mode, dir string, out *render.Output, pins map[string]string) (*Plan, error) {
	p := &Plan{Stack: name, Dir: dir, Mode: mode}
	for k := range out.Generated {
		p.Generated = append(p.Generated, k)
	}
	sort.Strings(p.Generated)
	man, err := readManifest(dir)
	if err != nil {
		return nil, err
	}
	p.First = man == nil
	pinChanges := map[string]bool{}
	var oldCompose, oldEnv []byte
	defer func() {
		if p != nil {
			p.addSteps(oldCompose, oldEnv, out, pinChanges)
		}
	}()
	paths := make([]string, 0, len(out.Files))
	for path := range out.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		old, err := os.ReadFile(filepath.Join(dir, path))
		switch {
		case errors.Is(err, os.ErrNotExist):
			p.Changes = append(p.Changes, Change{Path: path, Kind: Added, Diff: diff(nil, masked(out, path, out.Files[path]))})
		case err != nil:
			return nil, err
		case !bytes.Equal(old, out.Files[path]):
			p.Changes = append(p.Changes, Change{Path: path, Kind: Changed, Diff: diff(masked(out, path, old), masked(out, path, out.Files[path]))})
		}
	}
	if man != nil {
		for _, path := range man.Files {
			if _, ok := out.Files[path]; !ok {
				p.Changes = append(p.Changes, Change{Path: path, Kind: Removed})
			}
		}
		oldCompose, _ = os.ReadFile(filepath.Join(dir, render.ComposeFile))
		oldEnv, _ = os.ReadFile(filepath.Join(dir, render.EnvFile))
		p.Destructive = destructive(oldCompose, out.Files[render.ComposeFile])
		imgs := make([]string, 0, len(pins))
		for img := range pins {
			imgs = append(imgs, img)
		}
		sort.Strings(imgs)
		for _, img := range imgs {
			if was := man.Images[img]; was != pins[img] {
				d := "+ " + pins[img] + "\n"
				if was != "" {
					d = "- " + was + "\n" + d
				}
				p.Changes = append(p.Changes, Change{Path: "image " + img, Kind: Changed, Diff: d})
				pinChanges[img] = true
			}
		}
	}
	return p, nil
}

// masked hides secret values of an env file, in old and new content alike,
// and the current secret values wherever another file contains them.
func masked(out *render.Output, path string, content []byte) []byte {
	if path != render.EnvFile {
		return out.MaskSecrets(content)
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(string(content), "\n") {
		if k, _, ok := strings.Cut(line, "="); ok && out.Secret[k] {
			// A keyed mark lets a diff show *that* a secret changed, never
			// anything about its value.
			line = fmt.Sprintf("%s='•••••• (%s)'\n", k, fingerprint(line))
		}
		b.WriteString(line)
	}
	return []byte(b.String())
}

// destructive lists removals that can lose data or break dependants.
func destructive(oldCompose, newCompose []byte) []string {
	type doc struct {
		Services map[string]any `yaml:"services"`
		Volumes  map[string]any `yaml:"volumes"`
		Networks map[string]any `yaml:"networks"`
	}
	var o, n doc
	if yaml.Unmarshal(oldCompose, &o) != nil || yaml.Unmarshal(newCompose, &n) != nil {
		return nil
	}
	var out []string
	for _, sec := range []struct {
		what     string
		old, new map[string]any
	}{{"volume", o.Volumes, n.Volumes}, {"service", o.Services, n.Services}, {"network", o.Networks, n.Networks}} {
		var gone []string
		for k := range sec.old {
			if _, ok := sec.new[k]; !ok {
				gone = append(gone, k)
			}
		}
		sort.Strings(gone)
		for _, k := range gone {
			out = append(out, fmt.Sprintf("removes %s %q", sec.what, k))
		}
	}
	return out
}
