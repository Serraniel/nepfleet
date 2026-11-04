// SPDX-License-Identifier: AGPL-3.0-or-later

// Package template loads and validates NepFleet stack templates.
//
// The format is specified in docs/spec/template.md. A template is a directory
// holding template.yaml, a compose file and optional extra files.
package template

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// APIVersion is the only template format version this build understands.
const APIVersion = "nepfleet/v1alpha1"

// Builtins are variables NepFleet always provides to a compose file.
var Builtins = []string{"NEPFLEET_STACK", "NEPFLEET_INGRESS_NETWORK", "TZ"}

// Template is the parsed template.yaml.
type Template struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
	Spec       Spec     `yaml:"spec"`

	// Dir is the directory the template was loaded from.
	Dir string `yaml:"-"`
}

// Metadata describes the template for catalogues and the UI.
type Metadata struct {
	Name        string `yaml:"name"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Tier        string `yaml:"tier"` // system | app
	Category    string `yaml:"category"`
	Homepage    string `yaml:"homepage"`
	Source      string `yaml:"source"`
	Icon        string `yaml:"icon"`
}

// Spec is what NepFleet needs to install, update and migrate the stack.
type Spec struct {
	Version    string      `yaml:"version"`
	Compose    string      `yaml:"compose"`
	Images     []Image     `yaml:"images"`
	Supports   Supports    `yaml:"supports"`
	Inputs     []Input     `yaml:"inputs"`
	Generate   []Generate  `yaml:"generate"`
	Files      []File      `yaml:"files"`
	Volumes    []Volume    `yaml:"volumes"`
	Migrations []Migration `yaml:"migrations"`
}

// Image sets the update policy for one image the compose file uses. The tag
// itself lives only in the compose file, so there is one place to bump it.
type Image struct {
	// Name is the image without tag, as written in the compose file.
	Name string `yaml:"name"`
	// Track is the widest update NepFleet applies without a template change:
	// patch, minor or major. Anything wider opens an issue in the template repo.
	Track string `yaml:"track"`
}

// Supports flags which NepFleet integrations a template wires up.
type Supports struct {
	Swarm      bool `yaml:"swarm"`
	ARM64      bool `yaml:"arm64"`
	Ingress    bool `yaml:"ingress"`
	Monitoring bool `yaml:"monitoring"`
	StatusPage bool `yaml:"statusPage"`
	Dashboard  bool `yaml:"dashboard"`
	Logs       bool `yaml:"logs"`
	AutoUpdate bool `yaml:"autoUpdate"`
	Backup     bool `yaml:"backup"`
	SSO        bool `yaml:"sso"`
}

// Input is a value the wizard asks the user for.
type Input struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Required    bool     `yaml:"required"`
	Default     string   `yaml:"default"`
	Options     []string `yaml:"options"`
	Pattern     string   `yaml:"pattern"`
}

// Generate is a value NepFleet creates once at install time.
type Generate struct {
	Name     string `yaml:"name"`
	Kind     string `yaml:"kind"` // secret
	Length   int    `yaml:"length"`
	Encoding string `yaml:"encoding"`
}

// File is an extra file rendered into the stack directory.
type File struct {
	Src  string `yaml:"src"`
	Dest string `yaml:"dest"`
}

// Volume is persistent data the stack owns.
type Volume struct {
	Name   string `yaml:"name"`
	Backup bool   `yaml:"backup"`
	// Shared marks data that must follow the service across swarm nodes.
	Shared bool `yaml:"shared"`
}

// Migration moves an installed stack from one template version to another.
type Migration struct {
	From        string `yaml:"from"`
	To          string `yaml:"to"`
	Description string `yaml:"description"`
	Backup      string `yaml:"backup"`   // required | recommended | none
	Rollback    string `yaml:"rollback"` // restore | revert
	Steps       []Step `yaml:"steps"`
}

// Step is one action of a migration.
type Step struct {
	Name    string   `yaml:"name"`
	Service string   `yaml:"service"`
	Run     []string `yaml:"run"`
	Deploy  bool     `yaml:"deploy"`
	Wait    string   `yaml:"wait"` // healthy
}

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	envRe     = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	semverRe  = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$`)
	composeRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)`)

	inputTypes = []string{"string", "text", "secret", "hostname", "url", "email", "int", "bool", "enum", "path"}
	encodings  = []string{"", "base64url", "hex", "alnum"}
	tracks     = []string{"patch", "minor", "major"}
	tiers      = []string{"system", "app"}
)

// Load reads and strictly decodes dir/template.yaml. It does not validate.
func Load(dir string) (*Template, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "template.yaml"))
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var t Template
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, "template.yaml"), err)
	}
	t.Dir = dir
	return &t, nil
}

// Validate reports every problem found, not just the first.
func (t *Template) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if t.APIVersion != APIVersion {
		bad("apiVersion must be %q, got %q", APIVersion, t.APIVersion)
	}
	if t.Kind != "Template" {
		bad("kind must be Template, got %q", t.Kind)
	}
	m := t.Metadata
	if !nameRe.MatchString(m.Name) {
		bad("metadata.name %q must be lowercase letters, digits and dashes", m.Name)
	}
	if t.Dir != "" && filepath.Base(t.Dir) != m.Name {
		bad("metadata.name %q must match its directory %q", m.Name, filepath.Base(t.Dir))
	}
	if m.Title == "" || m.Description == "" {
		bad("metadata.title and metadata.description are required")
	}
	if !slices.Contains(tiers, m.Tier) {
		bad("metadata.tier must be one of %v", tiers)
	}

	s := t.Spec
	if !semverRe.MatchString(s.Version) {
		bad("spec.version %q is not a semantic version", s.Version)
	}

	if len(s.Images) == 0 {
		bad("spec.images must list at least one image")
	}
	for _, img := range s.Images {
		if img.Name == "" || strings.ContainsAny(img.Name, ":@") {
			bad("spec.images: name %q must be an image name without tag", img.Name)
		}
		if !slices.Contains(tracks, img.Track) {
			bad("spec.images %q: track must be one of %v", img.Name, tracks)
		}
	}

	declared := map[string]bool{}
	for _, b := range Builtins {
		declared[b] = true
	}
	declare := func(where, name string) {
		if !envRe.MatchString(name) {
			bad("%s %q must be an UPPER_SNAKE_CASE variable name", where, name)
		}
		if declared[name] {
			bad("%s %q is declared twice or shadows a builtin", where, name)
		}
		declared[name] = true
	}
	for _, in := range s.Inputs {
		declare("spec.inputs", in.Name)
		if !slices.Contains(inputTypes, in.Type) {
			bad("spec.inputs %q: type must be one of %v", in.Name, inputTypes)
		}
		if in.Type == "enum" && len(in.Options) == 0 {
			bad("spec.inputs %q: enum needs options", in.Name)
		}
		if in.Type == "enum" && in.Default != "" && !slices.Contains(in.Options, in.Default) {
			bad("spec.inputs %q: default %q is not an option", in.Name, in.Default)
		}
		if in.Pattern != "" {
			if _, err := regexp.Compile(in.Pattern); err != nil {
				bad("spec.inputs %q: pattern: %v", in.Name, err)
			}
		}
		if in.Type == "secret" && in.Default != "" {
			bad("spec.inputs %q: a secret must not have a default", in.Name)
		}
	}
	for _, g := range s.Generate {
		declare("spec.generate", g.Name)
		if g.Kind != "secret" {
			bad("spec.generate %q: kind must be secret", g.Name)
		}
		if g.Length < 16 || g.Length > 256 {
			bad("spec.generate %q: length must be between 16 and 256", g.Name)
		}
		if !slices.Contains(encodings, g.Encoding) {
			bad("spec.generate %q: encoding must be one of %v", g.Name, encodings[1:])
		}
	}

	if s.Compose == "" {
		bad("spec.compose is required")
	} else if t.Dir != "" {
		errs = append(errs, t.checkCompose(declared)...)
	}
	for _, f := range s.Files {
		if !local(f.Src) || !local(f.Dest) {
			bad("spec.files %q -> %q: paths must stay inside the template", f.Src, f.Dest)
		} else if t.Dir != "" {
			if _, err := os.Stat(filepath.Join(t.Dir, f.Src)); err != nil {
				bad("spec.files: %v", err)
			}
		}
	}
	for _, v := range s.Volumes {
		if !nameRe.MatchString(v.Name) {
			bad("spec.volumes %q: invalid name", v.Name)
		}
	}
	for i, mg := range s.Migrations {
		if mg.From == "" || !semverRe.MatchString(mg.To) {
			bad("spec.migrations[%d]: from is required and to must be a semantic version", i)
		}
		if !slices.Contains([]string{"required", "recommended", "none"}, mg.Backup) {
			bad("spec.migrations[%d]: backup must be required, recommended or none", i)
		}
		if !slices.Contains([]string{"restore", "revert"}, mg.Rollback) {
			bad("spec.migrations[%d]: rollback must be restore or revert", i)
		}
		if len(mg.Steps) == 0 {
			bad("spec.migrations[%d]: needs at least one step", i)
		}
	}
	return errors.Join(errs...)
}

// checkCompose ensures every ${VAR} the compose file uses is declared, so an
// install can never silently interpolate an empty string.
func (t *Template) checkCompose(declared map[string]bool) []error {
	if !local(t.Spec.Compose) {
		return []error{fmt.Errorf("spec.compose %q must stay inside the template", t.Spec.Compose)}
	}
	raw, err := os.ReadFile(filepath.Join(t.Dir, t.Spec.Compose))
	if err != nil {
		return []error{fmt.Errorf("spec.compose: %w", err)}
	}
	var doc struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return []error{fmt.Errorf("%s: %w", t.Spec.Compose, err)}
	}
	if len(doc.Services) == 0 {
		return []error{fmt.Errorf("%s: no services", t.Spec.Compose)}
	}
	var errs []error

	// Every image is pinned and has a policy; every policy is used.
	used := map[string]bool{}
	for name, svc := range doc.Services {
		if svc.Image == "" {
			errs = append(errs, fmt.Errorf("%s: service %s has no image (templates do not build)", t.Spec.Compose, name))
			continue
		}
		if err := checkImageRef(svc.Image); err != nil {
			errs = append(errs, fmt.Errorf("%s: service %s: %w", t.Spec.Compose, name, err))
		}
		img := ImageName(svc.Image)
		used[img] = true
		if !slices.ContainsFunc(t.Spec.Images, func(i Image) bool { return i.Name == img }) {
			errs = append(errs, fmt.Errorf("%s: service %s uses %s, which has no entry in spec.images", t.Spec.Compose, name, img))
		}
	}
	for _, i := range t.Spec.Images {
		if i.Name != "" && !used[i.Name] {
			errs = append(errs, fmt.Errorf("spec.images %q is not used by %s", i.Name, t.Spec.Compose))
		}
	}

	seen := map[string]bool{}
	for _, m := range composeRe.FindAllSubmatchIndex(raw, -1) {
		if m[0] > 0 && raw[m[0]-1] == '$' {
			continue // $${VAR} is an escaped literal, not interpolation
		}
		if v := string(raw[m[2]:m[3]]); !declared[v] && !seen[v] {
			seen[v] = true
			errs = append(errs, fmt.Errorf("%s uses ${%s}, which is neither an input, a generated value nor a builtin", t.Spec.Compose, v))
		}
	}
	return errs
}

// ImageName strips tag and digest from an image reference.
func ImageName(ref string) string {
	name, _, _ := strings.Cut(ref, "@")
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name = name[:i]
	}
	return name
}

func checkImageRef(ref string) error {
	if ref == "" {
		return errors.New("empty image ref")
	}
	name, _, digested := strings.Cut(ref, "@")
	last := name[strings.LastIndex(name, "/")+1:]
	_, tag, tagged := strings.Cut(last, ":")
	if !tagged && !digested {
		return fmt.Errorf("%q has no tag; pin one so updates are deliberate", ref)
	}
	if tag == "latest" {
		return fmt.Errorf("%q uses :latest; pin a version tag", ref)
	}
	return nil
}

func local(p string) bool {
	return p != "" && filepath.IsLocal(p)
}
