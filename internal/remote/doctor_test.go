// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"strings"
	"testing"
)

func TestParseDoctor(t *testing.T) {
	// JSON from doctor --json, also after other output.
	got := parseDoctor([]byte("warning: something\n" + `[{"name":"Docker","status":"ok","detail":"29.0"},{"name":"age key","status":"fail","detail":"no recipients\u001b[31m","fix":"add one"},{"name":"x","status":"weird"}]` + "\nsome checks failed\n"))
	if len(got) != 3 || got[1].Status != "fail" || got[1].Fix != "add one" || strings.Contains(got[1].Detail, "\x1b") || got[2].Status != "warn" {
		t.Errorf("json: %+v", got)
	}

	// The text of NepFleet versions without --json.
	text := "Host vps, 4 stack(s) planned here\n\n" +
		" ✓ Docker               29.9.3, compose v2.40\n" +
		" ✗ age key              fleet.yaml has no age recipients\n" +
		"                        → nepfleet key add-recipient\n" +
		" ⚠ Disk                 9 GiB free\n"
	got = parseDoctor([]byte(text))
	if len(got) != 3 || got[0].Name != "Docker" || got[0].Status != "ok" ||
		got[1].Name != "age key" || got[1].Status != "fail" || got[1].Fix != "nepfleet key add-recipient" ||
		got[2].Status != "warn" || got[2].Detail != "9 GiB free" {
		t.Errorf("text: %+v", got)
	}

	for out, want := range map[string]string{
		"nepfleet-doctor: stopped\n": "stopped",
		"nepfleet-doctor: absent\n":  "not installed",
		"sh: docker: not found\n":    "doctor didn't run",
	} {
		got := parseDoctor([]byte(out))
		if len(got) != 1 || got[0].Status != "fail" || !strings.Contains(got[0].Detail, want) {
			t.Errorf("%q: %+v", out, got)
		}
	}
}

func TestDoctorRunsTheScript(t *testing.T) {
	var script string
	run := func(_ context.Context, _ Target, s string) ([]byte, error) {
		script = s
		return []byte(`[{"name":"Docker","status":"ok"}]`), nil
	}
	checks, err := Doctor(context.Background(), run, Target{Host: "vps"})
	if err != nil || len(checks) != 1 {
		t.Fatalf("%v %v", checks, err)
	}
	if !strings.Contains(script, "docker exec") || !strings.Contains(script, "nepfleet doctor --json") {
		t.Errorf("script: %s", script)
	}
}
