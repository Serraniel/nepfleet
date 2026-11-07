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

// portMode reports a stack published on a LAN port.
func portMode(s *fleet.Stack) bool {
	return s.Spec.Access != nil && s.Spec.Access.Mode == "port"
}

// stackURL is ${NEPFLEET_URL}: where users reach the stack's web UI.
func stackURL(in Input, env map[string]string) string {
	if portMode(in.Stack) {
		addr := in.Stack.Spec.Target
		if in.Host != nil && in.Host.Spec.Address != "" {
			addr = in.Host.Spec.Address
		}
		return "http://" + addr + ":" + strconv.Itoa(in.Stack.Spec.Access.Port)
	}
	if d := env["DOMAIN"]; d != "" {
		return "https://" + d
	}
	return ""
}

// WithAccess publishes the web service on its LAN port in access mode port:
// Caddy's labels go (no ingress route), a ports entry comes in.
func WithAccess(compose []byte, t *template.Template, s *fleet.Stack) ([]byte, error) {
	if !portMode(s) {
		return compose, nil
	}
	if t == nil || t.Spec.Web == nil {
		return nil, fmt.Errorf("spec.access: mode port needs a template that declares its web UI (spec.web)")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty compose file")
	}
	svc := mapGet(mapGet(doc.Content[0], "services"), t.Spec.Web.Service)
	if svc == nil || svc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("spec.web: service %q is not in the compose file", t.Spec.Web.Service)
	}
	if labels := mapGet(svc, "labels"); labels != nil {
		dropCaddy(labels)
	}
	ports := mapGet(svc, "ports")
	if ports == nil {
		ports = &yaml.Node{Kind: yaml.SequenceNode}
		mapSet(svc, "ports", ports)
	}
	ports.Content = append(ports.Content, &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.DoubleQuotedStyle,
		Value: fmt.Sprintf("%d:%d", s.Spec.Access.Port, t.Spec.Web.Port)})
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// dropCaddy removes caddy, caddy.* and caddy_N… labels (map or list form).
func dropCaddy(labels *yaml.Node) {
	isCaddy := func(k string) bool {
		return k == "caddy" || strings.HasPrefix(k, "caddy.") || strings.HasPrefix(k, "caddy_")
	}
	switch labels.Kind {
	case yaml.MappingNode:
		var kept []*yaml.Node
		for i := 0; i+1 < len(labels.Content); i += 2 {
			if !isCaddy(labels.Content[i].Value) {
				kept = append(kept, labels.Content[i], labels.Content[i+1])
			}
		}
		labels.Content = kept
	case yaml.SequenceNode:
		var kept []*yaml.Node
		for _, n := range labels.Content {
			k, _, _ := strings.Cut(n.Value, "=")
			if !isCaddy(k) {
				kept = append(kept, n)
			}
		}
		labels.Content = kept
	}
}
