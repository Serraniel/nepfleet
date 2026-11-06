// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// isIntegration reports whether a label is read from swarm *service* labels
// by caddy-docker-proxy (caddy, caddy.*, caddy_N…), AutoKuma, Homepage,
// neptide, docker-volume-backup or Prometheus (nepfleet.metrics.*, read
// through dockerswarm_sd_configs).
func isIntegration(k string) bool {
	if k == "caddy" || strings.HasPrefix(k, "caddy.") || strings.HasPrefix(k, "caddy_") {
		return true
	}
	for _, p := range []string{"kuma.", "homepage.", "neptide.", "docker-volume-backup.", "nepfleet.metrics."} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// ForSwarm moves integration labels from services.*.labels to
// services.*.deploy.labels in an interpolated compose document, so templates
// write labels once and work in both modes, and adds safe rolling-update
// defaults (updateDefaults). Comments and order are kept.
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
		updateDefaults(svc)
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

// updateDefaults fills what a service's deploy.update_config and
// rollback_config leave unset: a failed update rolls back by itself
// (failure_action: rollback, watched for 30s), and stateless services start
// the new task before stopping the old one (order: start-first), so an
// update has no gap. Services with volumes or host-mode ports, and global
// services, keep stop-first: two tasks must not share data or a port.
func updateDefaults(svc *yaml.Node) {
	if svc == nil || svc.Kind != yaml.MappingNode {
		return
	}
	deploy := mapGet(svc, "deploy")
	if deploy == nil {
		deploy = &yaml.Node{Kind: yaml.MappingNode}
		mapSet(svc, "deploy", deploy)
	}
	if deploy.Kind != yaml.MappingNode {
		return
	}
	// Jobs run to completion; update settings mean nothing for them.
	if m := mapGet(deploy, "mode"); m != nil && strings.HasSuffix(m.Value, "-job") {
		return
	}
	order := "stop-first"
	if startFirstSafe(svc, deploy) {
		order = "start-first"
	}
	set := func(section string, kv ...string) {
		c := mapGet(deploy, section)
		if c == nil {
			c = &yaml.Node{Kind: yaml.MappingNode}
			mapSet(deploy, section, c)
		}
		if c.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(kv); i += 2 {
			if mapGet(c, kv[i]) == nil {
				mapSet(c, kv[i], &yaml.Node{Kind: yaml.ScalarNode, Value: kv[i+1]})
			}
		}
	}
	set("update_config", "failure_action", "rollback", "monitor", "30s", "order", order)
	set("rollback_config", "order", order)
}

// startFirstSafe: no volumes or binds, no host-mode ports, not global.
func startFirstSafe(svc, deploy *yaml.Node) bool {
	if m := mapGet(deploy, "mode"); m != nil && m.Value == "global" {
		return false
	}
	if v := mapGet(svc, "volumes"); v != nil && len(v.Content) > 0 {
		for _, vol := range v.Content {
			// tmpfs is per task; everything else may be shared.
			if t := mapGet(vol, "type"); t == nil || t.Value != "tmpfs" {
				return false
			}
		}
	}
	// On swarm's host network, two tasks would share the node's ports.
	if n := mapGet(svc, "networks"); n != nil && (mapGet(n, "host") != nil || slices.ContainsFunc(n.Content, func(c *yaml.Node) bool { return c.Kind == yaml.ScalarNode && c.Value == "host" })) {
		return false
	}
	if p := mapGet(svc, "ports"); p != nil {
		for _, port := range p.Content {
			if m := mapGet(port, "mode"); m != nil && m.Value == "host" {
				return false
			}
		}
	}
	return true
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
