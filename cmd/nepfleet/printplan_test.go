// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

func TestPrintPlanEgress(t *testing.T) {
	p := &deploy.Plan{Stack: "web", Mode: "compose", Changes: []deploy.Change{{Kind: "modified", Path: "compose.yaml"}}}
	for mode, want := range map[string]string{
		fleet.EgressEnforce: "egress: enforce",
		fleet.EgressLog:     "egress: log",
		"":                  "",
	} {
		p.Egress = mode
		var b bytes.Buffer
		printPlan(&b, p)
		if want == "" {
			if strings.Contains(b.String(), "egress") {
				t.Errorf("off says nothing:\n%s", b.String())
			}
		} else if !strings.Contains(b.String(), want) {
			t.Errorf("%s:\n%s", mode, b.String())
		}
	}
}
