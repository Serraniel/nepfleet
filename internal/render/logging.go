// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"bytes"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

// WithLogging adds a logging section to every service that has none, not
// even through a merge key (<<: *defaults). Services that use extends are
// left alone: their base, possibly in another file, may set logging.
// Comments and order are kept, and the file is written with a two-space
// indent like most compose files, so the diff stays small.
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
		if svc.Kind != yaml.MappingNode || hasLogging(svc, 0) || mapGet(svc, "extends") != nil {
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
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

// hasLogging reports a logging key on m, directly or through merge keys.
func hasLogging(m *yaml.Node, depth int) bool {
	if m == nil || depth > 10 {
		return false
	}
	if m.Kind == yaml.AliasNode {
		return hasLogging(m.Alias, depth+1)
	}
	if m.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		switch k.Value {
		case "logging":
			return true
		case "<<":
			if v.Kind == yaml.SequenceNode {
				for _, item := range v.Content {
					if hasLogging(item, depth+1) {
						return true
					}
				}
			} else if hasLogging(v, depth+1) {
				return true
			}
		}
	}
	return false
}
