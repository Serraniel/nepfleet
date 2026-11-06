// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

func TestFeatures(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range features {
		if seen[f.Name] || seen["tpl:"+f.Template] || !fleet.ValidName(f.Template) || f.What == "" {
			t.Errorf("feature %+v: duplicate or invalid", f)
		}
		seen[f.Name], seen["tpl:"+f.Template] = true, true
	}
}

func TestPositional(t *testing.T) {
	if p := positional([]string{"--target", "nas", "--set", "DOMAIN=x", "second"}); len(p) != 1 || p[0] != "second" {
		t.Errorf("positional = %v", p)
	}
	if p := positional([]string{"--set", "DOMAIN=x"}); len(p) != 0 {
		t.Errorf("positional = %v", p)
	}
}
