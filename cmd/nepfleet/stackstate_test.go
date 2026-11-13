// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"
)

func TestStackStates(t *testing.T) {
	got := stackStates([]container{
		{"web", "running", "Up 2 hours (healthy)"},
		{"web", "exited", "Exited (0) 2 hours ago"}, // a finished setup service
		{"db", "running", "Up 1 minute (unhealthy)"},
		{"db", "running", "Up 1 minute"},
		{"half", "running", "Up 3 days"},
		{"half", "exited", "Exited (137) 1 day ago"},
		{"off", "exited", "Exited (0) 1 day ago"},
		{"off", "created", "Created"},
		{"crash", "restarting", "Restarting (1) 5 seconds ago"},
	})
	want := map[string]string{"web": stateRunning, "db": stateUnhealthy, "half": statePartial, "off": stateStopped, "crash": stateStopped}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s: %q, want %q", p, got[p], w)
		}
	}
	if _, ok := got["none"]; ok || len(got) != len(want) {
		t.Errorf("extra states: %v", got)
	}
}

func TestPortFix(t *testing.T) {
	yes, no := func() bool { return true }, func() bool { return false }
	if got := portFix(53, yes); !strings.Contains(got, "BIND_ADDR") || !strings.Contains(got, "DNSStubListener=no") {
		t.Errorf("port 53 with the stub resolver: %q", got)
	}
	for _, c := range []struct {
		port int
		stub func() bool
	}{{53, no}, {80, yes}} {
		if got := portFix(c.port, c.stub); !strings.Contains(got, "ss -ltnp") {
			t.Errorf("port %d: %q", c.port, got)
		}
	}
}
