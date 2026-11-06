// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"bytes"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// WithBlockedHosts adds extra_hosts entries resolving hosts to 0.0.0.0 to
// every service. Services that share another container's network
// (network_mode: service:… or container:…) cannot have extra_hosts and are
// skipped; entries the service maps itself are kept as they are.
func WithBlockedHosts(compose []byte, hosts []string) ([]byte, error) {
	if len(hosts) == 0 {
		return compose, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return compose, nil
	}
	services := mapGet(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return compose, nil
	}
	for i := 1; i < len(services.Content); i += 2 {
		svc := services.Content[i]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		if nm := mapGet(svc, "network_mode"); nm != nil && (strings.HasPrefix(nm.Value, "service:") || strings.HasPrefix(nm.Value, "container:")) {
			continue
		}
		eh := mapGet(svc, "extra_hosts")
		switch {
		case eh == nil:
			eh = &yaml.Node{Kind: yaml.SequenceNode}
			mapSet(svc, "extra_hosts", eh)
		case eh.Kind != yaml.SequenceNode && eh.Kind != yaml.MappingNode:
			continue
		}
		for _, h := range hosts {
			if mapped(eh, h) {
				continue
			}
			if eh.Kind == yaml.MappingNode {
				mapSet(eh, h, &yaml.Node{Kind: yaml.ScalarNode, Value: "0.0.0.0", Style: yaml.DoubleQuotedStyle})
			} else {
				eh.Content = append(eh.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: h + ":0.0.0.0"})
			}
		}
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// mapped: the service already has an entry for host (list "host:ip" or
// "host=ip", or a map key), which wins.
func mapped(eh *yaml.Node, host string) bool {
	if eh.Kind == yaml.MappingNode {
		return mapGet(eh, host) != nil
	}
	return slices.ContainsFunc(eh.Content, func(n *yaml.Node) bool {
		return strings.HasPrefix(n.Value, host+":") || strings.HasPrefix(n.Value, host+"=")
	})
}
