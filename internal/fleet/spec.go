// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"go.yaml.in/yaml/v3"
)

// Fleet is fleet.yaml.
type Fleet struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec Spec `yaml:"spec"`

	Dir string `yaml:"-"`
}

// Spec holds the settings that apply to every stack.
type Spec struct {
	TemplateSources []Source `yaml:"templateSources"`
	Secrets         struct {
		// Age recipients (public keys) that every secret file is encrypted to.
		Age []string `yaml:"age"`
	} `yaml:"secrets"`
	Notifications struct {
		Channels map[string]map[string]any `yaml:"channels"`
		Routes   []map[string]any          `yaml:"routes"`
	} `yaml:"notifications"`
	Updates struct {
		Images string `yaml:"images"`
		Host   string `yaml:"host"`
	} `yaml:"updates"`
	// IngressNetwork is the external network Caddy and the stacks share.
	IngressNetwork string `yaml:"ingressNetwork"`
	// TZ is passed to stacks as ${TZ}.
	TZ string `yaml:"tz"`
}

// Source is a template catalogue: a git repository or a local directory.
type Source struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	Ref  string `yaml:"ref"`
	// Path is a local directory, relative to the fleet repository.
	Path string `yaml:"path"`
}

// Stack is stacks/<name>/stack.yaml (or system/<name>/stack.yaml).
type Stack struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec StackSpec `yaml:"spec"`

	// Dir is the stack's directory inside the fleet repository.
	Dir string `yaml:"-"`
}

// StackSpec says where and how a stack runs.
type StackSpec struct {
	Target   string            `yaml:"target"`
	Mode     string            `yaml:"mode"`  // compose | swarm
	Apply    string            `yaml:"apply"` // manual | auto
	Template *TemplateRef      `yaml:"template"`
	Values   map[string]string `yaml:"values"`
	Updates  string            `yaml:"updates"` // off | notify | auto
}

// TemplateRef selects a template from a source.
type TemplateRef struct {
	Source  string `yaml:"source"`
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

// SecretsFile is the encrypted values file next to stack.yaml.
const SecretsFile = "values.secret.yaml"

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func decodeStrict(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Load reads fleet.yaml from dir and applies defaults.
func Load(dir string) (*Fleet, error) {
	var f Fleet
	if err := decodeStrict(filepath.Join(dir, "fleet.yaml"), &f); err != nil {
		return nil, err
	}
	f.Dir = dir
	if f.Kind != "Fleet" {
		return nil, fmt.Errorf("fleet.yaml: kind must be Fleet")
	}
	if f.Spec.IngressNetwork == "" {
		f.Spec.IngressNetwork = "nepfleet_ingress"
	}
	if f.Spec.TZ == "" {
		f.Spec.TZ = "UTC"
	}
	seen := map[string]bool{}
	for _, s := range f.Spec.TemplateSources {
		if !nameRe.MatchString(s.Name) || seen[s.Name] {
			return nil, fmt.Errorf("fleet.yaml: template source name %q is invalid or duplicated", s.Name)
		}
		seen[s.Name] = true
		if (s.URL == "") == (s.Path == "") {
			return nil, fmt.Errorf("fleet.yaml: template source %q needs exactly one of url or path", s.Name)
		}
	}
	return &f, nil
}

// Source returns the named template source.
func (f *Fleet) Source(name string) (Source, bool) {
	for _, s := range f.Spec.TemplateSources {
		if s.Name == name {
			return s, true
		}
	}
	return Source{}, false
}

// StackDirs are the directories stacks live in, in apply order: system
// stacks (ingress, logs...) before apps.
var StackDirs = []string{"system", "stacks"}

// LoadStack reads one stack by name.
func (f *Fleet) LoadStack(name string) (*Stack, error) {
	for _, d := range StackDirs {
		dir := filepath.Join(f.Dir, d, name)
		if _, err := os.Stat(filepath.Join(dir, "stack.yaml")); err == nil {
			return loadStack(dir)
		}
	}
	return nil, fmt.Errorf("no stack %q in %v", name, StackDirs)
}

// Stacks reads every stack, system stacks first, each group sorted by name.
func (f *Fleet) Stacks() ([]*Stack, error) {
	var out []*Stack
	seen := map[string]string{}
	for _, d := range StackDirs {
		entries, err := os.ReadDir(filepath.Join(f.Dir, d))
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(f.Dir, d, e.Name())
			if _, err := os.Stat(filepath.Join(dir, "stack.yaml")); err != nil {
				continue
			}
			s, err := loadStack(dir)
			if err != nil {
				return nil, err
			}
			if prev, dup := seen[s.Metadata.Name]; dup {
				return nil, fmt.Errorf("stack %q exists in both %s and %s", s.Metadata.Name, prev, d)
			}
			seen[s.Metadata.Name] = d
			out = append(out, s)
		}
	}
	return out, nil
}

func loadStack(dir string) (*Stack, error) {
	var s Stack
	path := filepath.Join(dir, "stack.yaml")
	if err := decodeStrict(path, &s); err != nil {
		return nil, err
	}
	s.Dir = dir
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(path+": "+format, a...)) }
	if s.Kind != "Stack" {
		bad("kind must be Stack")
	}
	if s.Metadata.Name != filepath.Base(dir) || !nameRe.MatchString(s.Metadata.Name) {
		bad("metadata.name %q must be a valid name equal to the directory name", s.Metadata.Name)
	}
	if s.Spec.Target == "" {
		bad("spec.target is required (a host name, or local)")
	}
	if s.Spec.Mode == "" {
		s.Spec.Mode = "compose"
	}
	if !slices.Contains([]string{"compose", "swarm"}, s.Spec.Mode) {
		bad("spec.mode must be compose or swarm")
	}
	if s.Spec.Apply == "" {
		s.Spec.Apply = "manual"
	}
	if !slices.Contains([]string{"manual", "auto"}, s.Spec.Apply) {
		bad("spec.apply must be manual or auto")
	}
	if s.Spec.Updates == "" {
		s.Spec.Updates = "notify"
	}
	if !slices.Contains([]string{"off", "notify", "auto"}, s.Spec.Updates) {
		bad("spec.updates must be off, notify or auto")
	}
	if t := s.Spec.Template; t != nil && (t.Source == "" || t.Name == "") {
		bad("spec.template needs source and name")
	}
	if s.Spec.Template == nil {
		if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); err != nil {
			bad("a stack without spec.template needs a compose.yaml")
		}
	}
	return &s, errors.Join(errs...)
}
