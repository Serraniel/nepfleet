// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"go.yaml.in/yaml/v3"
)

// SetChannel makes name a notification channel whose shoutrrr URL is the
// fleet secret urlSecret, and, when no route sends to it yet, adds a route
// of events at minSeverity and above (all events of every stack). fleet.yaml
// is edited as YAML, so comments stay, and must load afterwards.
func SetChannel(dir, name, urlSecret, minSeverity string) error {
	return editFleet(dir, func(spec *yaml.Node) error {
		n, err := child(spec, "notifications", yaml.MappingNode)
		if err != nil {
			return err
		}
		channels, err := child(n, "channels", yaml.MappingNode)
		if err != nil {
			return err
		}
		ch := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "urlSecret"}, {Kind: yaml.ScalarNode, Value: urlSecret},
		}}
		mapPut(channels, name, ch)
		routes, err := child(n, "routes", yaml.SequenceNode)
		if err != nil {
			return err
		}
		for _, r := range routes.Content {
			if slices.Contains(routeChannels(r), name) {
				return nil // already routed; its routes stay as they are
			}
		}
		route := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "channels"},
			{Kind: yaml.SequenceNode, Style: yaml.FlowStyle, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: name}}},
		}}
		if minSeverity != "" {
			route.Content = append(route.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "minSeverity"}, &yaml.Node{Kind: yaml.ScalarNode, Value: minSeverity})
		}
		routes.Content = append(routes.Content, route)
		return nil
	}, func(f *Fleet) error {
		if f.Spec.Notifications.Channels[name].URLSecret != urlSecret {
			return errors.New("the channel isn't there after writing it")
		}
		return nil
	})
}

// RemoveChannel deletes the channel, takes it out of the routes and drops
// routes left without channels. It reports whether the channel existed.
func RemoveChannel(dir, name string) (bool, error) {
	found := false
	err := editFleet(dir, func(spec *yaml.Node) error {
		n := mapGet(spec, "notifications")
		if n == nil {
			return nil
		}
		if channels := mapGet(n, "channels"); channels != nil {
			found = mapDelete(channels, name)
		}
		if !found {
			return nil
		}
		routes := mapGet(n, "routes")
		if routes == nil || routes.Kind != yaml.SequenceNode {
			return nil
		}
		kept := routes.Content[:0]
		for _, r := range routes.Content {
			cs := mapGet(r, "channels")
			if cs != nil && cs.Kind == yaml.SequenceNode {
				cs.Content = slices.DeleteFunc(cs.Content, func(c *yaml.Node) bool { return c.Value == name })
				if len(cs.Content) == 0 {
					continue
				}
			}
			kept = append(kept, r)
		}
		routes.Content = kept
		return nil
	}, func(f *Fleet) error {
		if _, ok := f.Spec.Notifications.Channels[name]; ok {
			return errors.New("the channel is still there after removing it")
		}
		return nil
	})
	return found, err
}

// editFleet changes fleet.yaml's spec node and checks the fleet loads (and
// passes check) afterwards; otherwise the file is put back as it was.
func editFleet(dir string, edit func(spec *yaml.Node) error, check func(*Fleet) error) error {
	path := filepath.Join(dir, "fleet.yaml")
	raw, err := os.ReadFile(path) //nolint:gosec // the fleet's own file
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("fleet.yaml: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return errors.New("fleet.yaml is empty")
	}
	spec, err := child(doc.Content[0], "spec", yaml.MappingNode)
	if err != nil {
		return err
	}
	if err := edit(spec); err != nil {
		return err
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil { //nolint:gosec // the fleet's own file
		return err
	}
	f, err := Load(dir)
	if err == nil {
		err = check(f)
	}
	if err != nil {
		_ = os.WriteFile(path, raw, 0o600) //nolint:gosec // as it was
		return fmt.Errorf("editing fleet.yaml: %w", err)
	}
	return nil
}

// routeChannels lists a route node's channels.
func routeChannels(r *yaml.Node) []string {
	var out []string
	if cs := mapGet(r, "channels"); cs != nil && cs.Kind == yaml.SequenceNode {
		for _, c := range cs.Content {
			out = append(out, c.Value)
		}
	}
	return out
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

// mapPut sets key to v, replacing an existing value in place.
func mapPut(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
}

func mapDelete(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = slices.Delete(m.Content, i, i+2)
			return true
		}
	}
	return false
}
