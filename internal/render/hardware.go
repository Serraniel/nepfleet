// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"bytes"
	"fmt"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/template"
)

// WithHardware adds the devices and GPUs chosen in device and gpu inputs to
// their services. An empty input (or gpu none) adds nothing, so a template
// works without the hardware too.
//
// Swarm cannot map devices (docker stack deploy ignores devices and GPU
// reservations), so hardware on a swarm stack is an error rather than a
// stack that silently runs without it.
func WithHardware(compose []byte, hw []template.Hardware, env map[string]string, mode, renderGID string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return compose, nil
	}
	services := mapGet(doc.Content[0], "services")
	changed := false
	for _, h := range hw {
		v := env[h.Input]
		if v == "" || v == "none" {
			continue
		}
		if mode == "swarm" {
			return nil, fmt.Errorf("%s: swarm services cannot use host devices or GPUs (docker stack deploy ignores them); run this stack in compose mode or leave %s empty", h.Input, h.Input)
		}
		for _, name := range h.Services {
			svc := mapGet(services, name)
			if svc != nil && svc.Kind == yaml.AliasNode {
				return nil, fmt.Errorf("spec.hardware: service %q is an alias of another service; give it its own mapping (<<: *anchor) to add hardware", name)
			}
			if svc == nil || svc.Kind != yaml.MappingNode {
				return nil, fmt.Errorf("spec.hardware: service %q is not in the compose file", name)
			}
			switch v {
			case "intel", "amd":
				appendSeq(svc, "devices", "/dev/dri:/dev/dri")
				if renderGID != "" {
					// Non-root images need the render group; by number, as the
					// name means nothing inside the container.
					appendSeq(svc, "group_add", renderGID)
				}
			case "nvidia":
				if err := nvidia(svc); err != nil {
					return nil, err
				}
			default: // a device path
				target := h.Target
				if target == "" {
					target = v
				}
				appendSeq(svc, "devices", v+":"+target)
			}
			changed = true
		}
	}
	if !changed {
		return compose, nil
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

// appendSeq appends a scalar to the list m[key], creating it if needed. A
// list inherited through a merge key (<<: *base) is copied first: a direct
// key would replace it, not extend it.
func appendSeq(m *yaml.Node, key, value string) {
	seq := mapGet(m, key)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		seq = &yaml.Node{Kind: yaml.SequenceNode}
		if inherited := mergedSeq(m, key, 0); inherited != nil {
			seq.Content = append(seq.Content, inherited.Content...)
		}
		mapSet(m, key, seq)
	}
	for _, n := range seq.Content {
		if n.Value == value {
			return
		}
	}
	seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: value})
}

// nvidia reserves all NVIDIA GPUs (needs the NVIDIA container toolkit).
func nvidia(svc *yaml.Node) error {
	var res yaml.Node
	if err := res.Encode(map[string]any{"reservations": map[string]any{"devices": []map[string]any{
		{"driver": "nvidia", "count": "all", "capabilities": []string{"gpu"}},
	}}}); err != nil {
		return err
	}
	deploy := mapGet(svc, "deploy")
	if deploy == nil {
		deploy = &yaml.Node{Kind: yaml.MappingNode}
		mapSet(svc, "deploy", deploy)
	}
	if mapGet(deploy, "resources") != nil {
		return fmt.Errorf("spec.hardware: the service already sets deploy.resources; add the GPU there")
	}
	mapSet(deploy, "resources", &res)
	return nil
}

// mergedSeq finds the list m inherits for key through merge keys.
func mergedSeq(m *yaml.Node, key string, depth int) *yaml.Node {
	if m == nil || depth > 10 {
		return nil
	}
	if m.Kind == yaml.AliasNode {
		m = m.Alias
	}
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if depth > 0 && k.Value == key {
			if v.Kind == yaml.AliasNode {
				v = v.Alias
			}
			if v != nil && v.Kind == yaml.SequenceNode {
				return v
			}
		}
		if k.Value == "<<" {
			sources := []*yaml.Node{v}
			if v.Kind == yaml.SequenceNode {
				sources = v.Content
			}
			for _, src := range sources {
				if found := mergedSeq(src, key, depth+1); found != nil {
					return found
				}
			}
		}
	}
	return nil
}
