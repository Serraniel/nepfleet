// SPDX-License-Identifier: AGPL-3.0-or-later

package template

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Draft is a template generated from a compose file, for someone to finish.
type Draft struct {
	Template *Template
	Compose  []byte
	// Notes say what was guessed, and what needs a person.
	Notes []string
}

var (
	genRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::?[-?]([^}]*))?\}`)
	// Passwords the app only needs to agree on can be made up; keys from elsewhere can't.
	genSecretRe  = regexp.MustCompile(`(?i)(PASSWORD|PASSWD|PASS$|_PW$|SECRET|SALT|JWT|ENCRYPTION)`)
	askSecretRe  = regexp.MustCompile(`(?i)(TOKEN|API_?KEY|_KEY$|PRIVATE|CREDENTIAL|_DSN$)`)
	genDomainRe  = regexp.MustCompile(`(?i)^(DOMAIN|VIRTUAL_HOST|HOSTNAME|SERVER_NAME|PUBLIC_HOST)$`)
	genVolumeRe  = regexp.MustCompile(`[^a-z0-9-]+`)
	genWebPorts  = []int{80, 8080, 3000, 8000, 8096, 443, 8443, 9000, 5000}
	genNotWebRe  = regexp.MustCompile(`(?i)^(postgres|mysql|mariadb|redis|valkey|mongo|memcached|rabbitmq|elasticsearch)`)
	genPortRe    = regexp.MustCompile(`(?:^|:)(\d+)(?:/tcp)?$`)
	genSeenInput = func(in []Input, name string) bool {
		return slices.ContainsFunc(in, func(i Input) bool { return i.Name == name })
	}
)

func nodeChild(m *yaml.Node, key string) *yaml.Node {
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

func nodeSet(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
}

func nodeDelete(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = slices.Delete(m.Content, i, i+2)
			return
		}
	}
}

func text(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: s}
}

// guessInput fills in a type from a variable's name and default.
func guessInput(name, def string) Input {
	in := Input{Name: name, Type: "string", Title: strings.ReplaceAll(name, "_", " "), Default: def, Required: def == ""}
	switch {
	case genDomainRe.MatchString(name) || strings.HasSuffix(name, "_HOST") || strings.HasSuffix(name, "_DOMAIN"):
		in.Type = "hostname"
	case strings.HasSuffix(name, "_URL") || strings.HasSuffix(name, "_URI"):
		in.Type = "url"
	case strings.HasSuffix(name, "EMAIL"):
		in.Type = "email"
	case strings.HasSuffix(name, "_PORT") || name == "PUID" || name == "PGID":
		in.Type = "int"
	case strings.HasSuffix(name, "_PATH") || strings.HasSuffix(name, "_DIR"):
		in.Type = "path"
	case def == "true" || def == "false":
		in.Type = "bool"
	}
	return in
}

type generator struct {
	t     *Template
	notes []string
}

func (g *generator) note(format string, args ...any) {
	g.notes = append(g.notes, fmt.Sprintf(format, args...))
}

// secret declares a variable holding a secret, made up or asked for.
func (g *generator) secret(name string) bool {
	switch {
	case slices.Contains(Builtins, name) || genSeenInput(g.t.Spec.Inputs, name):
		return true
	case slices.ContainsFunc(g.t.Spec.Generate, func(x Generate) bool { return x.Name == name }):
		return true
	case genSecretRe.MatchString(name):
		g.t.Spec.Generate = append(g.t.Spec.Generate, Generate{Name: name, Kind: "secret", Length: 32})
		return true
	case askSecretRe.MatchString(name):
		g.t.Spec.Inputs = append(g.t.Spec.Inputs, Input{Name: name, Type: "secret", Title: strings.ReplaceAll(name, "_", " "), Required: true})
		return true
	}
	return false
}

func (g *generator) environment(service string, svc *yaml.Node) {
	env := nodeChild(svc, "environment")
	if env == nil {
		return
	}
	move := func(key, value string) (string, bool) {
		if strings.Contains(value, "${") || value == "" {
			return "", false
		}
		name := strings.ToUpper(key)
		if !envRe.MatchString(name) {
			return "", false
		}
		if genDomainRe.MatchString(key) {
			if !genSeenInput(g.t.Spec.Inputs, "DOMAIN") {
				g.t.Spec.Inputs = append(g.t.Spec.Inputs, guessInput("DOMAIN", ""))
			}
			return "${DOMAIN}", true
		}
		if !genSecretRe.MatchString(key) && !askSecretRe.MatchString(key) {
			return "", false
		}
		g.secret(name)
		g.note("service %s: %s looked like a secret: its value isn't in the template, and it's now ${%s}", service, key, name)
		return "${" + name + "}", true
	}
	switch env.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(env.Content); i += 2 {
			if v, ok := move(env.Content[i].Value, env.Content[i+1].Value); ok {
				env.Content[i+1] = text(v)
			}
		}
	case yaml.SequenceNode:
		for _, item := range env.Content {
			k, val, found := strings.Cut(item.Value, "=")
			if !found {
				continue
			}
			if v, ok := move(k, val); ok {
				item.Value = k + "=" + v
			}
		}
	}
}

func imageName(image string) (name, tag string) {
	image, _, _ = strings.Cut(image, "@")
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[:i], image[i+1:]
	}
	return image, ""
}

func (g *generator) image(service string, svc *yaml.Node) error {
	img := nodeChild(svc, "image")
	if img == nil {
		if nodeChild(svc, "build") != nil {
			return fmt.Errorf("service %s builds its own image: a template runs published images, so publish it first", service)
		}
		return fmt.Errorf("service %s has no image", service)
	}
	if strings.Contains(img.Value, "${") {
		g.note("service %s: its image comes from a variable, so it isn't in spec.images; write the image there", service)
		return nil
	}
	name, tag := imageName(img.Value)
	if tag == "" || tag == "latest" {
		g.note("service %s: %s has no fixed tag; set a version", service, img.Value)
	}
	if !slices.ContainsFunc(g.t.Spec.Images, func(i Image) bool { return i.Name == name }) {
		g.t.Spec.Images = append(g.t.Spec.Images, Image{Name: name, Track: "minor"})
	}
	return nil
}

func containerPorts(svc *yaml.Node) []int {
	ports := nodeChild(svc, "ports")
	if ports == nil || ports.Kind != yaml.SequenceNode {
		return nil
	}
	var out []int
	for _, p := range ports.Content {
		var raw string
		if p.Kind == yaml.MappingNode {
			if t := nodeChild(p, "target"); t != nil {
				raw = t.Value
			}
		} else {
			raw = p.Value
		}
		if m := genPortRe.FindStringSubmatch(raw); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// web picks the service and port most likely to be the web UI, and moves it from
// a published port to the ingress, as templates do.
func (g *generator) web(doc, services *yaml.Node) {
	type candidate struct {
		name string
		svc  *yaml.Node
		port int
		rank int
	}
	var best *candidate
	for i := 0; i+1 < len(services.Content); i += 2 {
		svc := services.Content[i+1]
		img := nodeChild(svc, "image")
		if img != nil && genNotWebRe.MatchString(img.Value[strings.LastIndex(img.Value, "/")+1:]) {
			continue
		}
		for _, p := range containerPorts(svc) {
			rank := slices.Index(genWebPorts, p)
			if rank < 0 {
				rank = len(genWebPorts)
			}
			if best == nil || rank < best.rank {
				best = &candidate{services.Content[i].Value, svc, p, rank}
			}
		}
	}
	if best == nil {
		g.note("no published port looks like a web UI: add spec.web and Caddy labels if it has one")
		return
	}
	g.t.Spec.Web = &Web{Service: best.name, Port: best.port}
	g.t.Spec.Supports.Ingress = true
	if !genSeenInput(g.t.Spec.Inputs, "DOMAIN") {
		g.t.Spec.Inputs = append(g.t.Spec.Inputs, guessInput("DOMAIN", ""))
	}
	g.note("the web UI is taken to be service %s, port %d: check that, and that it speaks plain HTTP", best.name, best.port)

	// Its port goes; access modes publish it. Others stay, for a person to judge.
	ports := nodeChild(best.svc, "ports")
	kept := ports.Content[:0:0]
	for _, p := range ports.Content {
		single := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "ports"}, {Kind: yaml.SequenceNode, Content: []*yaml.Node{p}}}}
		if !slices.Contains(containerPorts(single), best.port) {
			kept = append(kept, p)
			g.note("service %s still publishes %s: remove it unless clients need that port directly", best.name, portText(p))
		}
	}
	if len(kept) == 0 {
		nodeDelete(best.svc, "ports")
	} else {
		ports.Content = kept
	}

	labels := nodeChild(best.svc, "labels")
	if labels == nil {
		labels = &yaml.Node{Kind: yaml.MappingNode}
		nodeSet(best.svc, "labels", labels)
	}
	add := func(k, v string) {
		if labels.Kind == yaml.SequenceNode {
			labels.Content = append(labels.Content, text(k+"="+v))
			return
		}
		nodeSet(labels, k, text(v))
	}
	add("caddy", "${DOMAIN}")
	add("caddy.reverse_proxy", fmt.Sprintf("{{upstreams %d}}", best.port))

	nets := nodeChild(best.svc, "networks")
	switch {
	case nets == nil:
		nodeSet(best.svc, "networks", &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "default"}, {Kind: yaml.ScalarNode, Value: "ingress"}}})
	case nets.Kind == yaml.SequenceNode:
		nets.Content = append(nets.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "ingress"})
	case nets.Kind == yaml.MappingNode:
		nodeSet(nets, "ingress", &yaml.Node{Kind: yaml.MappingNode})
	}
	top := nodeChild(doc, "networks")
	if top == nil {
		top = &yaml.Node{Kind: yaml.MappingNode}
		nodeSet(doc, "networks", top)
	}
	ingress := &yaml.Node{Kind: yaml.MappingNode}
	nodeSet(ingress, "name", &yaml.Node{Kind: yaml.ScalarNode, Value: "${NEPFLEET_INGRESS_NETWORK}"})
	nodeSet(ingress, "external", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
	nodeSet(top, "ingress", ingress)
}

func portText(p *yaml.Node) string {
	if p.Kind == yaml.ScalarNode {
		return p.Value
	}
	if t := nodeChild(p, "target"); t != nil {
		return "port " + t.Value
	}
	return "a port"
}

func (g *generator) volumes(doc, services *yaml.Node) {
	top := nodeChild(doc, "volumes")
	renamed := map[string]string{}
	if top != nil && top.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(top.Content); i += 2 {
			key := top.Content[i].Value
			name := strings.Trim(genVolumeRe.ReplaceAllString(strings.ToLower(key), "-"), "-")
			if name != key {
				renamed[key] = name
				top.Content[i].Value = name
			}
			if name != "" && !slices.ContainsFunc(g.t.Spec.Volumes, func(v Volume) bool { return v.Name == name }) {
				g.t.Spec.Volumes = append(g.t.Spec.Volumes, Volume{Name: name, Backup: true})
			}
		}
		if len(g.t.Spec.Volumes) > 0 {
			g.t.Spec.Supports.Backup = true
			g.note("all named volumes are marked for backup; set backup: false for caches, and snapshot: false for large media")
		}
	}
	for i := 0; i+1 < len(services.Content); i += 2 {
		service, svc := services.Content[i].Value, services.Content[i+1]
		if p := nodeChild(svc, "privileged"); p != nil && p.Value == "true" {
			g.note("service %s is privileged, which is effectively root on the host: narrow it to the capabilities it needs", service)
		}
		vols := nodeChild(svc, "volumes")
		if vols == nil || vols.Kind != yaml.SequenceNode {
			continue
		}
		for _, v := range vols.Content {
			var src string
			var srcNode *yaml.Node
			if v.Kind == yaml.MappingNode {
				srcNode = nodeChild(v, "source")
				if srcNode != nil {
					src = srcNode.Value
				}
			} else {
				src, _, _ = strings.Cut(v.Value, ":")
			}
			switch {
			case src == "/var/run/docker.sock":
				g.note("service %s mounts the Docker socket, which is effectively root on the host: put a socket proxy in front", service)
			case strings.HasPrefix(src, "/") || strings.HasPrefix(src, "."):
				g.note("service %s mounts the host path %s: use a named volume, or a path input", service, src)
			case renamed[src] != "":
				if srcNode != nil {
					srcNode.Value = renamed[src]
				} else {
					v.Value = renamed[src] + strings.TrimPrefix(v.Value, src)
				}
			}
		}
	}
}

// refs declares what the compose file refers to. A secret's default stays out, so
// it's never written down.
func (g *generator) refs(compose []byte) ([]byte, error) {
	var bad []string
	out := genRefRe.ReplaceAllStringFunc(string(compose), func(m string) string {
		sub := genRefRe.FindStringSubmatch(m)
		name, def := sub[1], sub[2]
		switch {
		case slices.Contains(Builtins, name):
		case !envRe.MatchString(name):
			bad = append(bad, name)
		case g.secret(name):
			if def != "" && (genSecretRe.MatchString(name) || askSecretRe.MatchString(name)) {
				g.note("${%s} had a default, left out as it looks like a secret", name)
				return "${" + name + "}"
			}
		default:
			g.t.Spec.Inputs = append(g.t.Spec.Inputs, guessInput(name, def))
		}
		return m
	})
	if len(bad) > 0 {
		slices.Sort(bad)
		return nil, fmt.Errorf("variables must be UPPER_SNAKE_CASE in a template: rename %s", strings.Join(slices.Compact(bad), ", "))
	}
	return []byte(out), nil
}

// DraftFrom drafts a template from a compose file. It never carries secrets over,
// and leaves judgement calls in Notes.
func DraftFrom(compose []byte, name string) (*Draft, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("%q can't be a template name: lowercase letters, digits and -", name)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(compose, &root); err != nil {
		return nil, fmt.Errorf("compose file: %w", err)
	}
	if len(root.Content) == 0 {
		return nil, errors.New("the compose file is empty")
	}
	doc := root.Content[0]
	services := nodeChild(doc, "services")
	if services == nil || services.Kind != yaml.MappingNode || len(services.Content) == 0 {
		return nil, errors.New("the compose file has no services")
	}
	nodeDelete(doc, "version") // obsolete
	nodeDelete(doc, "name")    // NepFleet names the project after the stack

	g := &generator{t: &Template{
		APIVersion: "nepfleet/v1alpha1", Kind: "Template",
		Metadata: Metadata{Name: name, Title: name, Description: "Generated from a compose file: describe what this does.", Tier: "app"},
		Spec:     Spec{Version: "0.1.0", Compose: "compose.yaml"},
	}}
	for i := 0; i+1 < len(services.Content); i += 2 {
		service, svc := services.Content[i].Value, services.Content[i+1]
		if err := g.image(service, svc); err != nil {
			return nil, err
		}
		g.environment(service, svc)
		if cn := nodeChild(svc, "container_name"); cn != nil {
			nodeDelete(svc, "container_name")
			g.note("service %s: container_name is removed, so two stacks from this template can coexist", service)
		}
	}
	g.web(doc, services)
	g.volumes(doc, services)
	raw, err := yaml.Marshal(&root)
	if err != nil {
		return nil, err
	}
	raw, err = g.refs(raw)
	if err != nil {
		return nil, err
	}
	if len(g.t.Spec.Images) == 0 {
		return nil, errors.New("no service names a fixed image; a template needs at least one in spec.images")
	}
	g.note("the track of each image is set to minor: check how each project versions")
	g.note("generated secrets are made up at install; if the app takes a secret from elsewhere, make it a secret input")
	return &Draft{Template: g.t, Compose: raw, Notes: g.notes}, nil
}
