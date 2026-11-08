// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/egress"
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
