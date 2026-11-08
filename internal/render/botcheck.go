// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

// anubisImage checks visitors. Pinned by digest: it sits in front of the app.
const anubisImage = "ghcr.io/techarohq/anubis:v1.28.2@sha256:5fcbce70ee6168be5b226d19bec90111d0acd0daf13028fa06b044cf3ebbbd5f"

// anubisPort is where Anubis listens, on the stack's networks only.
const anubisPort = "8923"

// AnubisPolicyFile is the rendered policy, next to the compose file.
const AnubisPolicyFile = "anubis-policy.yaml"

// anubisPolicy allows the stack's own paths first, then Anubis's own rules
// (known bad bots denied, good crawlers and well-known paths allowed), and
// checks everything that looks like a browser. No DNS blocklist lookups:
// they would tell a third party who visits.
func anubisPolicy(b *fleet.BotCheck) []byte {
	var s strings.Builder
	s.WriteString("# Rendered by NepFleet from the stack's spec.botCheck.\nbots:\n")
	for i, p := range b.Allow {
		fmt.Fprintf(&s, "  - name: nepfleet-allow-%d\n    path_regex: %s\n    action: ALLOW\n", i+1, strconv.Quote(p))
	}
	s.WriteString("  - import: (data)/meta/default-config.yaml\n")
	fmt.Fprintf(&s, "  - name: nepfleet-browsers\n    user_agent_regex: \"Mozilla|Opera\"\n    action: CHALLENGE\n    challenge:\n      algorithm: fast\n      difficulty: %d\n", b.Difficulty)
	s.WriteString("dnsbl: false\n")
	return []byte(s.String())
}

// WithBotCheck puts Anubis between Caddy and the template's web service: Caddy's
// labels move to Anubis, which forwards what passes to the service.
func WithBotCheck(compose []byte, t *template.Template, s *fleet.Stack, out *Output) ([]byte, error) {
	b := s.Spec.BotCheck
	if b == nil || !b.Enabled {
		return compose, nil
	}
	switch {
	case s.Spec.Mode == "swarm":
		return nil, fmt.Errorf("spec.botCheck: not supported for swarm stacks yet")
	case portMode(s):
		return nil, fmt.Errorf("spec.botCheck: needs access mode domain; in mode port, Caddy isn't in front")
	case t == nil || t.Spec.Web == nil:
		return nil, fmt.Errorf("spec.botCheck: needs a template that declares its web UI (spec.web)")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty compose file")
	}
	services := mapGet(doc.Content[0], "services")
	web := t.Spec.Web.Service
	svc := mapGet(services, web)
	if svc == nil || svc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("spec.botCheck: the web service %s is not in the compose file", web)
	}
	name := web + "-anubis"
	if mapGet(services, name) != nil {
		return nil, fmt.Errorf("spec.botCheck: a service is already called %s", name)
	}
	if _, ok := out.Files[AnubisPolicyFile]; ok {
		return nil, fmt.Errorf("spec.botCheck: a template file is already called %s", AnubisPolicyFile)
	}
	labels := mapGet(svc, "labels")
	var moved *yaml.Node
	if labels != nil {
		moved = takeCaddyLabels(labels)
	}
	if moved == nil {
		return nil, fmt.Errorf("spec.botCheck: service %s has no Caddy labels to put Anubis behind", web)
	}
	caddy, err := anubisLabels(moved, t.Spec.Web.Port)
	if err != nil {
		return nil, err
	}

	side := &yaml.Node{Kind: yaml.MappingNode}
	mapSet(side, "image", str(anubisImage))
	mapSet(side, "restart", str("unless-stopped"))
	env := &yaml.Node{Kind: yaml.MappingNode}
	mapSet(env, "BIND", str(":"+anubisPort))
	mapSet(env, "TARGET", str("http://"+web+":"+strconv.Itoa(t.Spec.Web.Port)))
	mapSet(env, "POLICY_FNAME", str("/data/cfg/"+AnubisPolicyFile))
	mapSet(env, "SERVE_ROBOTS_TXT", str("false"))
	mapSet(side, "environment", env)
	mapSet(side, "volumes", seq("./"+AnubisPolicyFile+":/data/cfg/"+AnubisPolicyFile+":ro"))
	mapSet(side, "read_only", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
	mapSet(side, "tmpfs", seq("/tmp"))
	mapSet(side, "cap_drop", seq("ALL"))
	mapSet(side, "security_opt", seq("no-new-privileges:true"))
	// The same networks as the service: the ingress, for Caddy, and the one
	// Anubis reaches the service on.
	if nets := mapGet(svc, "networks"); nets != nil {
		mapSet(side, "networks", nets)
	}
	mapSet(side, "labels", caddy)
	services.Content = append(services.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: name}, side)
	out.Files[AnubisPolicyFile] = anubisPolicy(b)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// anubisLabels turns the web service's Caddy labels into Anubis's: the one route
// to the service goes to Anubis instead, which needs the visitor's address.
func anubisLabels(moved *yaml.Node, port int) (*yaml.Node, error) {
	out := &yaml.Node{Kind: yaml.MappingNode}
	add := func(k, v string) { mapSet(out, k, str(v)) }
	var pairs [][2]string
	switch moved.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(moved.Content); i += 2 {
			pairs = append(pairs, [2]string{moved.Content[i].Value, moved.Content[i+1].Value})
		}
	case yaml.SequenceNode:
		for _, n := range moved.Content {
			k, v, _ := strings.Cut(n.Value, "=")
			pairs = append(pairs, [2]string{k, v})
		}
	}
	want := fmt.Sprintf("{{upstreams %d}}", port)
	routed := false
	for _, kv := range pairs {
		k, v := kv[0], kv[1]
		if strings.Contains(v, "{{upstreams") {
			// One plain route to the web port: anything else (paths sent to
			// other ports, several routes) can't be told apart safely.
			if k != "caddy.reverse_proxy" || strings.TrimSpace(v) != want || routed {
				return nil, fmt.Errorf("spec.botCheck: supports one plain route, caddy.reverse_proxy: %q; this service has %s: %q", want, k, v)
			}
			add(k, "{{upstreams "+anubisPort+"}}")
			routed = true
			continue
		}
		if k == "caddy.reverse_proxy.header_up" {
			return nil, fmt.Errorf("spec.botCheck: the service sets %s already, which Anubis needs for the visitor's address", k)
		}
		add(k, v)
	}
	if !routed {
		return nil, fmt.Errorf("spec.botCheck: the web service's labels have no caddy.reverse_proxy: %q", want)
	}
	add("caddy.reverse_proxy.header_up", "X-Real-Ip {remote_host}")
	return out, nil
}
