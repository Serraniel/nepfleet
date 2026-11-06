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
// secrets ({from, value|secret}), by key. secret marks values that come
// from a secret anywhere along the chain, so they are masked.
func (e *env) resolveRefs(s *fleet.Stack) (values map[string]string, secret map[string]bool, err error) {
	values, secret = map[string]string{}, map[string]bool{}
	keys := make([]string, 0, len(s.Spec.Values))
	for k := range s.Spec.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	r := refResolver{e: e, secrets: map[string]map[string]string{}}
	for _, k := range keys {
		v := s.Spec.Values[k]
		if !v.IsRef() {
			continue
		}
		if v.From == s.Metadata.Name {
			return nil, nil, fmt.Errorf("%s: a stack cannot take values from itself", k)
		}
		val, isSecret, err := r.value(v, []string{s.Metadata.Name})
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", k, err)
		}
		values[k], secret[k] = val, isSecret
	}
	return values, secret, nil
}

// refResolver decrypts each source stack's secrets at most once.
type refResolver struct {
	e       *env
	secrets map[string]map[string]string
}

// value is the value a reference points at; seen holds the stacks on the
// way there, so a chain of references cannot loop.
func (r refResolver) value(v fleet.Value, seen []string) (string, bool, error) {
	if slices.Contains(seen, v.From) {
		return "", false, fmt.Errorf("references loop: %s → %s", strings.Join(seen, " → "), v.From)
	}
	src, err := r.e.fleet.LoadStack(v.From)
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", v, err)
	}
	if v.Secret != "" {
		sec, err := r.load(src)
		if err != nil {
			return "", false, fmt.Errorf("%s: %w", v, err)
		}
		if x, ok := sec[v.Secret]; ok {
			return x, true, nil
		}
		return "", false, fmt.Errorf("%s is not set (generated secrets exist once %s has been applied; set others with nepfleet secret set)", v, v.From)
	}
	if raw, ok := src.Spec.Values[v.Value]; ok {
		if raw.IsRef() {
			return r.value(raw, append(seen, v.From))
		}
		return raw.Literal, false, nil
	}
	if src.Spec.Template != nil {
		// Only value references need the template (defaults, hints), so a
		// secret reference works offline with an uncached source too.
		tpl, _, err := r.e.resolver.Template(*src.Spec.Template)
		if err != nil {
			return "", false, fmt.Errorf("%s: %w", v, err)
		}
		for _, inp := range tpl.Spec.Inputs {
			if inp.Name != v.Value {
				continue
			}
			if inp.Type == "secret" {
				return "", false, fmt.Errorf("%s is a secret input: use {from: %s, secret: %s}", v, v.From, v.Value)
			}
			if inp.Default != "" {
				return inp.Default, false, nil
			}
		}
		for _, g := range tpl.Spec.Generate {
			if g.Name == v.Value {
				return "", false, fmt.Errorf("%s is a generated secret: use {from: %s, secret: %s}", v, v.From, v.Value)
			}
		}
	}
	return "", false, fmt.Errorf("%s is not set", v)
}

func (r refResolver) load(s *fleet.Stack) (map[string]string, error) {
	if sec, ok := r.secrets[s.Metadata.Name]; ok {
		return sec, nil
	}
	sec, err := r.e.store.Load(filepath.Join(s.Dir, fleet.SecretsFile))
	if err != nil {
		return nil, err
	}
	r.secrets[s.Metadata.Name] = sec
	return sec, nil
}
