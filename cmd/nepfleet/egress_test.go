// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/egress"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

func TestPrintSuggestion(t *testing.T) {
	r := egress.Report{Hosts: []string{"api.example.org"}, Addresses: []string{"203.0.113.9"}, LookedUp: []string{"cdn.example.org"}}
	var b bytes.Buffer
	printSuggestion(&b, "web", r, false)
	out := b.String()
	if !strings.Contains(out, "what it did") || !strings.Contains(out, "- api.example.org") || !strings.Contains(out, "- 203.0.113.9") || !strings.Contains(out, "cdn.example.org") {
		t.Errorf("log mode:\n%s", out)
	}
	b.Reset()
	printSuggestion(&b, "web", r, true)
	out = b.String()
	if !strings.Contains(out, "refused for web") || !strings.Contains(out, "- api.example.org") || strings.Contains(out, "cdn.example.org") {
		t.Errorf("enforce mode lists what was refused, not lookups:\n%s", out)
	}
	b.Reset()
	printSuggestion(&b, "web", egress.Report{}, true)
	if !strings.Contains(b.String(), "nothing of web's was refused") {
		t.Errorf("nothing refused: %s", b.String())
	}
}

func TestEgressCheck(t *testing.T) {
	stack := func(name, mode string) *fleet.Stack {
		s := &fleet.Stack{}
		s.Metadata.Name = name
		if mode != "" {
			s.Spec.Egress = &fleet.Egress{Mode: mode}
		}
		return s
	}
	f := &fleet.Fleet{}
	if _, ok := egressCheck(f, []*fleet.Stack{stack("a", "")}); ok {
		t.Error("nothing to say when every stack is off")
	}
	c, ok := egressCheck(f, []*fleet.Stack{stack("web", "enforce"), stack("db", "log"), stack("x", "")})
	if !ok || c.Detail != "enforced: web; recorded: db" || !strings.Contains(c.Fix, "egress suggest") {
		t.Errorf("%+v", c)
	}
}
