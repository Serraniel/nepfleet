// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/api"
)

// mappingChild finds key in a YAML mapping, adding it with an empty node of
// kind when it isn't there.
func mappingChild(m *yaml.Node, key string, kind yaml.Kind) (*yaml.Node, error) {
	if m.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: not a mapping", key)
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
				v.Kind, v.Tag, v.Value = kind, "", ""
			}
			if v.Kind != kind {
				return nil, fmt.Errorf("%s is not what it should be", key)
			}
			return v, nil
		}
	}
	v := &yaml.Node{Kind: kind}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
	return v, nil
}

// addEgressAllow adds entries to spec.egress.allow of a stack.yaml, keeping
// its comments, and says which were new.
func addEgressAllow(raw []byte, add []string) ([]byte, []string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, nil, errors.New("stack.yaml is empty")
	}
	spec, err := mappingChild(doc.Content[0], "spec", yaml.MappingNode)
	if err != nil {
		return nil, nil, err
	}
	eg, err := mappingChild(spec, "egress", yaml.MappingNode)
	if err != nil {
		return nil, nil, err
	}
	allow, err := mappingChild(eg, "allow", yaml.SequenceNode)
	if err != nil {
		return nil, nil, err
	}
	allow.Style = 0 // a block list, as people write it
	var have, added []string
	for _, n := range allow.Content {
		have = append(have, n.Value)
	}
	for _, a := range add {
		if !slices.Contains(have, a) {
			allow.Content = append(allow.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: a})
			have = append(have, a)
			added = append(added, a)
		}
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, nil, err
	}
	return b.Bytes(), added, enc.Close()
}

// egressAllowAPI adds hosts or addresses to a stack's egress allow list and
// commits it as by. It doesn't apply.
func egressAllowAPI(c common, stack string, add []string, by string) (api.EgressAllowResult, error) {
	if !apiMu.TryLock() {
		return api.EgressAllowResult{}, api.ErrBusy
	}
	defer apiMu.Unlock()
	e, err := c.open()
	if err != nil {
		return api.EgressAllowResult{}, err
	}
	s, err := e.fleet.LoadStack(stack)
	if err != nil {
		return api.EgressAllowResult{}, api.ErrNotFound
	}
	path := filepath.Join(s.Dir, "stack.yaml")
	rel := relTo(e.fleet.Dir, path)
	st, err := gitOut(e.fleet.Dir, "status", "--porcelain", "--", rel)
	if err != nil {
		return api.EgressAllowResult{}, err
	}
	if strings.TrimSpace(st) != "" {
		return api.EgressAllowResult{}, api.ErrUncommitted
	}
	raw, err := os.ReadFile(path) //nolint:gosec // a stack's own file
	if err != nil {
		return api.EgressAllowResult{}, err
	}
	out, added, err := addEgressAllow(raw, add)
	if err != nil {
		return api.EgressAllowResult{}, api.InvalidError{Reason: "stack.yaml: " + err.Error()}
	}
	if len(added) == 0 {
		return api.EgressAllowResult{Added: []string{}}, nil
	}
	if err := os.WriteFile(path, out, 0o600); err != nil { //nolint:gosec // a stack's own file
		return api.EgressAllowResult{}, err
	}
	restore := func() { _ = os.WriteFile(path, raw, 0o600) } //nolint:gosec // as it was
	if _, err := e.fleet.LoadStack(stack); err != nil {
		restore()
		return api.EgressAllowResult{}, api.InvalidError{Reason: cleanReason(err, e.fleet.Dir)}
	}
	msg := "chore(" + stack + "): allow egress to " + strings.Join(added, ", ")
	if err := commitAs(e.fleet.Dir, apiAuthor(by), msg, rel); err != nil {
		restore()
		return api.EgressAllowResult{}, err
	}
	head, err := gitOut(e.fleet.Dir, "rev-parse", "HEAD")
	return api.EgressAllowResult{Commit: strings.TrimSpace(head), Added: added}, err
}
