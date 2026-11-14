// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"strings"
	"testing"
)

func TestUpdatePlan(t *testing.T) {
	compose := []byte("services:\n  nepfleet:\n    image: ghcr.io/serraniel/nepfleet:0.4.0\n")
	r := &Report{NepFleetImage: "ghcr.io/serraniel/nepfleet:0.3.1", NepFleetRunning: true}
	if got := r.RunningVersion(); got != "0.3.1" {
		t.Errorf("RunningVersion = %q", got)
	}
	steps, err := UpdatePlan(r, compose, "v0.4.0")
	if err != nil || len(steps) != 4 {
		t.Fatalf("update: %v %v", steps, err)
	}
	all := ""
	for _, s := range steps {
		all += s.Text + "\n" + s.Script
		if s.Root {
			t.Errorf("%q needs no root: the folder is the user's", s.Text)
		}
	}
	for _, want := range []string{"compose.yaml.previous", "nepfleet:0.4.0", "docker compose up -d --wait", "start 0.3.1 again", ".env"} {
		if !strings.Contains(all, want) {
			t.Errorf("plan lacks %q", want)
		}
	}

	for _, c := range []struct {
		r       *Report
		version string
		want    string
	}{
		{&Report{NepFleetImage: "ghcr.io/serraniel/nepfleet:0.4.0"}, "0.4.0", "already"},
		{&Report{NepFleetImage: "ghcr.io/serraniel/nepfleet:0.5.0"}, "0.4.0", "already"},
		{&Report{}, "0.4.0", "no NepFleet container"},
		{&Report{NepFleetImage: "ghcr.io/serraniel/nepfleet:edge"}, "0.4.0", "can't tell"},
		{r, "0.4.0-dev+abc", "development build"},
	} {
		if _, err := UpdatePlan(c.r, compose, c.version); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v → %s: want %q, got %v", c.r, c.version, c.want, err)
		}
	}
}

func TestStartPlan(t *testing.T) {
	steps, err := StartPlan(&Report{})
	if err != nil || len(steps) != 1 || !strings.Contains(steps[0].Script, "docker compose up -d --wait") || steps[0].Root {
		t.Fatalf("start: %+v %v", steps, err)
	}
	if _, err := StartPlan(&Report{NepFleetImage: "ghcr.io/serraniel/nepfleet:0.4.0", NepFleetRunning: true}); err == nil {
		t.Error("started a running NepFleet")
	}
	if got := parseDoctor([]byte("nepfleet-doctor: stopped\n")); len(got) != 1 || got[0].Action != "start" {
		t.Errorf("stopped check: %+v", got)
	}
}
