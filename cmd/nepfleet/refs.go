// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

// resolveRefs resolves a stack's references to other stacks' values and
// secrets ({from, value|secret}), by key.
func (e *env) resolveRefs(s *fleet.Stack) (map[string]string, error) {
	out := map[string]string{}
	keys := make([]string, 0, len(s.Spec.Values))
	for k := range s.Spec.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := s.Spec.Values[k]
		if !v.IsRef() {
			continue
		}
		r, err := e.refValue(v, []string{s.Metadata.Name})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = r
	}
	return out, nil
}

// refValue is the value a reference points at; seen holds the stacks on the
// way there, so a chain of references cannot loop.
func (e *env) refValue(v fleet.Value, seen []string) (string, error) {
	if slices.Contains(seen, v.From) {
		return "", fmt.Errorf("references loop: %s → %s", strings.Join(seen, " → "), v.From)
	}
	src, err := e.fleet.LoadStack(v.From)
	if err != nil {
		return "", fmt.Errorf("%s: %w", v, err)
	}
	if v.Secret != "" {
		sec, err := e.store.Load(filepath.Join(src.Dir, fleet.SecretsFile))
		if err != nil {
			return "", fmt.Errorf("%s: %w", v, err)
		}
		if x, ok := sec[v.Secret]; ok {
			return x, nil
		}
		return "", fmt.Errorf("%s is not set (generated secrets exist once %s has been applied; set others with nepfleet secret set)", v, v.From)
	}
	if raw, ok := src.Spec.Values[v.Value]; ok {
		if raw.IsRef() {
			return e.refValue(raw, append(seen, v.From))
		}
		return raw.Literal, nil
	}
	if src.Spec.Template != nil {
		tpl, _, err := e.resolver.Template(*src.Spec.Template)
		if err != nil {
			return "", fmt.Errorf("%s: %w", v, err)
		}
		for _, inp := range tpl.Spec.Inputs {
			if inp.Name == v.Value && inp.Type != "secret" && inp.Default != "" {
				return inp.Default, nil
			}
		}
	}
	return "", fmt.Errorf("%s is not set", v)
}
