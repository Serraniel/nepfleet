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
	APIVersion string   `yaml:"apiVersion" schema:"required;const=nepfleet/v1alpha1"`
	Kind       string   `yaml:"kind" schema:"required;const=Template"`
	Metadata   Metadata `yaml:"metadata" schema:"required"`
	Spec       Spec     `yaml:"spec" schema:"required"`

	// Dir is the directory the template was loaded from.
	Dir string `yaml:"-"`
}

// Metadata describes the template for catalogues and the UI.
type Metadata struct {
	Name        string `yaml:"name" schema:"required;pattern=^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$" json:"name,omitempty"`
	Title       string `yaml:"title" schema:"required" json:"title,omitempty"`
	Description string `yaml:"description" schema:"required" json:"description,omitempty"`
	Tier        string `yaml:"tier" schema:"required;enum=system|app" json:"tier,omitempty"` // system | app
	Category    string `yaml:"category" json:"category,omitempty"`
	Homepage    string `yaml:"homepage" json:"homepage,omitempty"`
	Source      string `yaml:"source" json:"source,omitempty"`
	Icon        string `yaml:"icon" json:"icon,omitempty"`
}

// Spec is what NepFleet needs to install, update and migrate the stack.
type Spec struct {
	Version    string      `yaml:"version" schema:"required;pattern=^(0|[1-9]\\d*)\\.(0|[1-9]\\d*)\\.(0|[1-9]\\d*)(-[0-9A-Za-z.-]+)?$"`
	Compose    string      `yaml:"compose" schema:"required"`
	Images     []Image     `yaml:"images" schema:"required;minItems=1"`
	Supports   Supports    `yaml:"supports"`
	Inputs     []Input     `yaml:"inputs"`
	Generate   []Generate  `yaml:"generate"`
	Files      []File      `yaml:"files"`
	Volumes    []Volume    `yaml:"volumes"`
	Migrations []Migration `yaml:"migrations"`
	// Hardware maps device and gpu inputs to services; NepFleet adds them
	// to the rendered compose file only when the input has a value.
	Hardware []Hardware `yaml:"hardware"`
}

// Hardware connects one device or gpu input to services.
type Hardware struct {
	// Input names a spec.inputs entry of type device or gpu.
	Input string `yaml:"input" schema:"required"`
	// Services get the device or GPU.
	Services []string `yaml:"services" schema:"required;minItems=1"`
	// Target is the device path inside the container (device inputs;
	// default: the same path as on the host).
	Target string `yaml:"target"`
}

// Image sets the update policy for one image the compose file uses. The tag
// itself lives only in the compose file, so there is one place to bump it.
type Image struct {
	// Name is the image without tag, as written in the compose file.
	Name string `yaml:"name" schema:"required;pattern=^[^:@]+$" json:"name,omitempty"`
	// Track is the widest update NepFleet applies without a template change:
	// patch, minor or major. Anything wider opens an issue in the template repo.
	Track string `yaml:"track" schema:"required;enum=patch|minor|major" json:"track,omitempty"`
}

// Supports flags which NepFleet integrations a template wires up.
type Supports struct {
	Swarm      bool `yaml:"swarm" json:"swarm"`
	ARM64      bool `yaml:"arm64" json:"arm64"`
	Ingress    bool `yaml:"ingress" json:"ingress"`
	Monitoring bool `yaml:"monitoring" json:"monitoring"`
	StatusPage bool `yaml:"statusPage" json:"statusPage"`
	Dashboard  bool `yaml:"dashboard" json:"dashboard"`
	Logs       bool `yaml:"logs" json:"logs"`
	AutoUpdate bool `yaml:"autoUpdate" json:"autoUpdate"`
	Backup     bool `yaml:"backup" json:"backup"`
	SSO        bool `yaml:"sso" json:"sso"`
}

// Input is a value the wizard asks the user for.
type Input struct {
	Name        string   `yaml:"name" schema:"required;pattern=^[A-Z][A-Z0-9_]*$" json:"name,omitempty"`
	Type        string   `yaml:"type" schema:"required;enum=string|text|secret|hostname|url|email|int|bool|enum|path|device|gpu" json:"type,omitempty"`
	Title       string   `yaml:"title" json:"title,omitempty"`
	Description string   `yaml:"description" json:"description,omitempty"`
	Required    bool     `yaml:"required" json:"required,omitempty"`
	Default     string   `yaml:"default" json:"default,omitempty"`
	Options     []string `yaml:"options" json:"options,omitempty"`
	Pattern     string   `yaml:"pattern" json:"pattern,omitempty"`
}

// Generate is a value NepFleet creates once at install time.
type Generate struct {
	Name     string `yaml:"name" schema:"required;pattern=^[A-Z][A-Z0-9_]*$" json:"name,omitempty"`
	Kind     string `yaml:"kind" schema:"required;enum=secret" json:"kind,omitempty"` // secret
	Length   int    `yaml:"length" schema:"required;minimum=16;maximum=256" json:"length,omitempty"`
	Encoding string `yaml:"encoding" schema:"enum=base64url|hex|alnum" json:"encoding,omitempty"`
}

// File is an extra file rendered into the stack directory.
type File struct {
	Src  string `yaml:"src" schema:"required"`
	Dest string `yaml:"dest" schema:"required"`
}

// Volume is persistent data the stack owns.
type Volume struct {
	Name   string `yaml:"name" schema:"required;pattern=^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$" json:"name,omitempty"`
	Backup bool   `yaml:"backup" json:"backup,omitempty"`
	// Snapshot: false keeps the volume out of the data snapshot taken
	// before an apply (large media, caches).
	Snapshot *bool `yaml:"snapshot" json:"snapshot,omitempty"`
	// Shared marks data that must follow the service across swarm nodes.
	Shared bool `yaml:"shared" json:"shared,omitempty"`
}

// Migration moves an installed stack from one template version to another.
type Migration struct {
	From        string `yaml:"from" schema:"required"`
	To          string `yaml:"to" schema:"required;pattern=^(0|[1-9]\\d*)\\.(0|[1-9]\\d*)\\.(0|[1-9]\\d*)(-[0-9A-Za-z.-]+)?$"`
	Description string `yaml:"description"`
	Backup      string `yaml:"backup" schema:"required;enum=required|recommended|none"` // required | recommended | none
	Rollback    string `yaml:"rollback" schema:"required;enum=restore|revert"`          // restore | revert
	Steps       []Step `yaml:"steps" schema:"required;minItems=1"`
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

	inputTypes    = []string{"string", "text", "secret", "hostname", "url", "email", "int", "bool", "enum", "path", "device", "gpu"}
	gpuKinds      = []string{"none", "intel", "amd", "nvidia"}
	encodings     = []string{"", "base64url", "hex", "alnum"}
	tracks        = []string{"patch", "minor", "major"}
	tiers         = []string{"system", "app"}
	generateKinds = []string{"secret"}
	backups       = []string{"required", "recommended", "none"}
	rollbacks     = []string{"restore", "revert"}
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
		if !slices.Contains(generateKinds, g.Kind) {
			bad("spec.generate %q: kind must be secret", g.Name)
		}
		if g.Length < 16 || g.Length > 256 {
			bad("spec.generate %q: length must be between 16 and 256", g.Name)
		}
		if !slices.Contains(encodings, g.Encoding) {
			bad("spec.generate %q: encoding must be one of %v", g.Name, encodings[1:])
		}
	}

	byName := map[string]Input{}
	for _, in := range s.Inputs {
		byName[in.Name] = in
	}
	for i, h := range s.Hardware {
		in, ok := byName[h.Input]
		if !ok || (in.Type != "device" && in.Type != "gpu") {
			bad("spec.hardware[%d]: input %q must be a declared input of type device or gpu", i, h.Input)
		}
		if len(h.Services) == 0 {
			bad("spec.hardware[%d]: services is required", i)
		}
		if h.Target != "" && (!ValidDevice(h.Target) || in.Type == "gpu") {
			bad("spec.hardware[%d]: target must be a /dev path, for device inputs only", i)
		}
		if in.Type == "gpu" && in.Default != "" && !slices.Contains(gpuKinds, in.Default) {
			bad("spec.inputs %q: a gpu default must be one of %v", in.Name, gpuKinds)
		}
		if in.Type == "device" && in.Default != "" && !ValidDevice(in.Default) {
			bad("spec.inputs %q: default is not a device path", in.Name)
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
		if !slices.Contains(backups, mg.Backup) {
			bad("spec.migrations[%d]: backup must be required, recommended or none", i)
		}
		if !slices.Contains(rollbacks, mg.Rollback) {
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
			Image  string `yaml:"image"`
			Labels any    `yaml:"labels"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return []error{fmt.Errorf("%s: %w", t.Spec.Compose, err)}
	}
	if len(doc.Services) == 0 {
		return []error{fmt.Errorf("%s: no services", t.Spec.Compose)}
	}
	var errs []error
	for i, h := range t.Spec.Hardware {
		for _, name := range h.Services {
			if _, ok := doc.Services[name]; !ok {
				errs = append(errs, fmt.Errorf("spec.hardware[%d]: service %q is not in %s", i, name, t.Spec.Compose))
			}
		}
	}

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

	// Integration badges must be true: a flag needs its labels, labels need their flag.
	keys := map[string]bool{}
	for _, svc := range doc.Services {
		for _, k := range labelKeys(svc.Labels) {
			keys[k] = true
		}
	}
	for _, in := range []struct {
		flag   string
		on     bool
		match  func(string) bool
		labels string
	}{
		{"ingress", t.Spec.Supports.Ingress, func(k string) bool {
			return k == "caddy" || strings.HasPrefix(k, "caddy.") || strings.HasPrefix(k, "caddy_")
		}, "caddy"},
		{"monitoring", t.Spec.Supports.Monitoring, func(k string) bool { return strings.HasPrefix(k, "kuma.") }, "kuma.*"},
		{"dashboard", t.Spec.Supports.Dashboard, func(k string) bool { return strings.HasPrefix(k, "homepage.") }, "homepage.*"},
	} {
		has := false
		for k := range keys {
			if in.match(k) {
				has = true
				break
			}
		}
		switch {
		case in.on && !has:
			errs = append(errs, fmt.Errorf("supports.%s is true, but no service has %s labels", in.flag, in.labels))
		case !in.on && has:
			errs = append(errs, fmt.Errorf("services have %s labels, but supports.%s is false", in.labels, in.flag))
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

// labelKeys accepts both compose label forms: a map or a "k=v" list.
func labelKeys(labels any) []string {
	var out []string
	switch l := labels.(type) {
	case map[string]any:
		for k := range l {
			out = append(out, k)
		}
	case []any:
		for _, item := range l {
			if s, ok := item.(string); ok {
				k, _, _ := strings.Cut(s, "=")
				out = append(out, k)
			}
		}
	}
	return out
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

// deviceRe allows the characters of real device names (including
// /dev/serial/by-id names) but not ":", which compose's device syntax
// splits on, nor "$", whitespace or newlines.
var deviceRe = regexp.MustCompile(`^/dev/[A-Za-z0-9._+@/-]+$`)

// ValidDevice reports a clean device path under /dev.
func ValidDevice(p string) bool {
	return deviceRe.MatchString(p) && p == filepath.Clean(p)
}
