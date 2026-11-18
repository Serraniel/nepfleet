// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/doctor"
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

	// An edge build updates from a release, or from an older edge, without
	// comparing versions: edge is the newest main.
	edgeCompose := []byte("services:\n  nepfleet:\n    image: ghcr.io/serraniel/nepfleet:edge@sha256:" + strings.Repeat("a", 64) + "\n")
	for image, current := range map[string]string{
		"ghcr.io/serraniel/nepfleet:0.5.0":                                  "0.5.0",
		"ghcr.io/serraniel/nepfleet:edge@sha256:" + strings.Repeat("b", 64): "the previous edge",
	} {
		steps, err := UpdatePlan(&Report{NepFleetImage: image, NepFleetRunning: true}, edgeCompose, EdgeVersion)
		if err != nil || len(steps) != 4 || !strings.Contains(steps[3].Text, "start "+current+" again") {
			t.Errorf("edge from %s: %+v %v", image, steps, err)
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

func TestRepairPlan(t *testing.T) {
	if _, err := RepairPlan(&Report{}); err == nil {
		t.Error("repaired a machine without a NepFleet container")
	}
	steps, err := RepairPlan(&Report{NepFleetImage: "ghcr.io/serraniel/nepfleet:0.4.0", NepFleetRunning: true})
	if err != nil || len(steps) != 3 {
		t.Fatalf("repair: %+v %v", steps, err)
	}
	for _, want := range []string{"if [ ! -s age.key ]", "key add-recipient", "rev-parse -q --verify HEAD", "docker compose up -d --wait"} {
		found := false
		for _, s := range steps {
			found = found || strings.Contains(s.Script, want)
		}
		if !found {
			t.Errorf("no step does %q", want)
		}
	}
	for _, s := range steps {
		if s.Root {
			t.Errorf("%q needs no root", s.Text)
		}
	}
}

// The app offers the repair for exactly the failures it fixes, and never
// for an action the machine names itself.
func TestDoctorRepairAction(t *testing.T) {
	out := `[{"name":"age key","status":"fail","detail":"` + doctor.NoRecipients + `"},` +
		`{"name":"Fleet repository","status":"warn","detail":"` + doctor.NoCommits + `"},` +
		`{"name":"age key","status":"fail","detail":"no private key at /x"},` +
		`{"name":"Disk","status":"warn","detail":"full","action":"repair"}]`
	got := parseDoctor([]byte(out))
	want := []string{"repair", "repair", "", ""}
	if len(got) != len(want) {
		t.Fatalf("checks: %+v", got)
	}
	for i, w := range want {
		if got[i].Action != w {
			t.Errorf("%s (%s): action %q, want %q", got[i].Name, got[i].Detail, got[i].Action, w)
		}
	}
}

func TestDoctorDNSAction(t *testing.T) {
	got := parseDoctor([]byte(`[{"name":"DNS","status":"fail","detail":"x","action":"dns-records"},{"name":"DNS","status":"ok","detail":"y","action":"dns-records"},{"name":"Disk","status":"fail","detail":"z","action":"dns-records"}]`))
	if len(got) != 3 || got[0].Action != "dns-records" || got[1].Action != "" || got[2].Action != "" {
		t.Errorf("%+v", got)
	}
}
