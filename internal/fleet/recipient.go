// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"go.yaml.in/yaml/v3"
)

// ageRecipient is an age public key (bech32, "age1" and 58 characters).
var ageRecipient = regexp.MustCompile(`^age1[02-9ac-hj-np-z]{58}$`)

// AddRecipient makes the fleet's secrets encrypt to pub (an age public key)
// too: spec.secrets.age in fleet.yaml, edited as YAML so comments stay. It
// checks that the fleet then loads with pub among its recipients, and
// reports whether it added it (false: it was there already).
func AddRecipient(dir, pub string) (bool, error) {
	if !ageRecipient.MatchString(pub) {
		return false, fmt.Errorf("%q is not an age public key (age1…)", pub)
	}
	path := filepath.Join(dir, "fleet.yaml")
	raw, err := os.ReadFile(path) //nolint:gosec // the fleet's own file
	if err != nil {
		return false, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return false, fmt.Errorf("fleet.yaml: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return false, errors.New("fleet.yaml is empty")
	}
	node := doc.Content[0]
	for _, key := range []string{"spec", "secrets"} {
		if node, err = child(node, key, yaml.MappingNode); err != nil {
			return false, err
		}
	}
	age, err := child(node, "age", yaml.SequenceNode)
	if err != nil {
		return false, err
	}
	if slices.ContainsFunc(age.Content, func(n *yaml.Node) bool { return n.Value == pub }) {
		return false, nil
	}
	age.Content = append(age.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: pub, Style: yaml.DoubleQuotedStyle})
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return false, err
	}
	if err := enc.Close(); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil { //nolint:gosec // the fleet's own file
		return false, err
	}
	f, err := Load(dir)
	if err != nil || !slices.Contains(f.Spec.Secrets.Age, pub) {
		_ = os.WriteFile(path, raw, 0o600) //nolint:gosec // as it was
		if err == nil {
			err = errors.New("the key isn't among the fleet's recipients after writing it")
		}
		return false, fmt.Errorf("adding the recipient: %w", err)
	}
	return true, nil
}

// child finds key in a mapping, adding it (or filling an empty value) with a
// node of kind.
func child(m *yaml.Node, key string, kind yaml.Kind) (*yaml.Node, error) {
	if m.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("fleet.yaml: %s's parent isn't a mapping", key)
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}
		v := m.Content[i+1]
		if v.Kind == yaml.ScalarNode && (v.Tag == "!!null" || v.Value == "") {
			v.Kind, v.Tag, v.Value = kind, "", ""
		}
		if v.Kind != kind {
			return nil, fmt.Errorf("fleet.yaml: %s isn't what it should be", key)
		}
		v.Style = 0 // a block list or mapping, as people write it
		return v, nil
	}
	v := &yaml.Node{Kind: kind}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
	return v, nil
}
