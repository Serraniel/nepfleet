// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"errors"
	"slices"

	"go.yaml.in/yaml/v3"
)

// AddMirror appends u to spec.mirrors, keeping fleet.yaml's comments. It
// reports false, and changes nothing, when u is there already.
func AddMirror(dir, u string) (bool, error) {
	added := false
	err := editFleet(dir, func(spec *yaml.Node) error {
		m, err := child(spec, "mirrors", yaml.SequenceNode)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(m.Content, func(n *yaml.Node) bool { return n.Value == u }) {
			return nil
		}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: u, Style: yaml.DoubleQuotedStyle})
		added = true
		return nil
	}, func(f *Fleet) error {
		if !slices.Contains(f.Spec.Mirrors, u) {
			return errors.New("the mirror isn't there after writing it")
		}
		return nil
	})
	return added && err == nil, err
}

// RemoveMirror takes the mirrors match accepts out of spec.mirrors and
// returns them.
func RemoveMirror(dir string, match func(u string) bool) ([]string, error) {
	var removed []string
	err := editFleet(dir, func(spec *yaml.Node) error {
		m := mapGet(spec, "mirrors")
		if m == nil || m.Kind != yaml.SequenceNode {
			return nil
		}
		m.Content = slices.DeleteFunc(m.Content, func(n *yaml.Node) bool {
			if match(n.Value) {
				removed = append(removed, n.Value)
				return true
			}
			return false
		})
		return nil
	}, func(f *Fleet) error {
		if slices.ContainsFunc(f.Spec.Mirrors, match) {
			return errors.New("the mirror is still there after removing it")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return removed, nil
}
