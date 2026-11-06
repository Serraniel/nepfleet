// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

// isIntegration reports whether a label is read from swarm *service* labels
// by caddy-docker-proxy (caddy, caddy.*, caddy_N…), AutoKuma, Homepage,
// neptide or docker-volume-backup.
func isIntegration(k string) bool {
	if k == "caddy" || strings.HasPrefix(k, "caddy.") || strings.HasPrefix(k, "caddy_") {
		return true
	}
	for _, p := range []string{"kuma.", "homepage.", "neptide.", "docker-volume-backup."} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// ForSwarm moves integration labels from services.*.labels to
// services.*.deploy.labels in an interpolated compose document, so templates
// write labels once and work in both modes. Comments and order are kept.
func ForSwarm(compose []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty compose document")
	}
	services := mapGet(doc.Content[0], "services")
	if services == nil {
		return nil, fmt.Errorf("compose document has no services")
	}
	for i := 1; i < len(services.Content); i += 2 {
		svc := services.Content[i]
		labels := mapGet(svc, "labels")
		if labels == nil {
			continue
		}
		if labels.Kind == yaml.SequenceNode {
			labels = listToMap(labels)
			mapSet(svc, "labels", labels)
		}
		var keep, move []*yaml.Node
		for j := 0; j+1 < len(labels.Content); j += 2 {
			k := labels.Content[j].Value
			if isIntegration(k) {
				move = append(move, labels.Content[j], labels.Content[j+1])
			} else {
				keep = append(keep, labels.Content[j], labels.Content[j+1])
			}
		}
		if len(move) == 0 {
			continue
		}
		labels.Content = keep
		if len(keep) == 0 {
			mapDelete(svc, "labels")
		}
		deploy := mapGet(svc, "deploy")
		if deploy == nil {
			deploy = &yaml.Node{Kind: yaml.MappingNode}
			mapSet(svc, "deploy", deploy)
		}
		dl := mapGet(deploy, "labels")
		if dl == nil {
			dl = &yaml.Node{Kind: yaml.MappingNode}
			mapSet(deploy, "labels", dl)
		} else if dl.Kind == yaml.SequenceNode {
			dl = listToMap(dl)
			mapSet(deploy, "labels", dl)
		}
		dl.Content = append(dl.Content, move...)
	}
	return yaml.Marshal(&doc)
}

// PinImages rewrites services.*.image values found in pins to image@digest.
func PinImages(compose []byte, pins map[string]string) ([]byte, error) {
	if len(pins) == 0 {
		return compose, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty compose document")
	}
	services := mapGet(doc.Content[0], "services")
	for i := 1; services != nil && i < len(services.Content); i += 2 {
		if img := mapGet(services.Content[i], "image"); img != nil {
			if d, ok := pins[img.Value]; ok {
				img.Value += "@" + d
			}
		}
	}
	return yaml.Marshal(&doc)
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
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

func mapSet(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
}

func mapDelete(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// listToMap converts ["a=b", "c"] labels to {a: b, c: ""}.
func listToMap(seq *yaml.Node) *yaml.Node {
	out := &yaml.Node{Kind: yaml.MappingNode}
	for _, item := range seq.Content {
		k, v, _ := strings.Cut(item.Value, "=")
		out.Content = append(out.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: k},
			&yaml.Node{Kind: yaml.ScalarNode, Value: v, Style: yaml.DoubleQuotedStyle})
	}
	return out
}

// WithLogging adds a logging section to every service that has none.
// Services with their own logging keep it; comments and order are kept.
func WithLogging(compose []byte, l *fleet.Logging) ([]byte, error) {
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
	changed := false
	for i := 1; i < len(services.Content); i += 2 {
		svc := services.Content[i]
		if svc.Kind != yaml.MappingNode || mapGet(svc, "logging") != nil {
			continue
		}
		var node yaml.Node
		section := map[string]any{"driver": l.Driver}
		if len(l.Options) > 0 {
			section["options"] = l.Options
		}
		if err := node.Encode(section); err != nil {
			return nil, err
		}
		mapSet(svc, "logging", &node)
		changed = true
	}
	if !changed {
		return compose, nil
	}
	return yaml.Marshal(&doc)
}
