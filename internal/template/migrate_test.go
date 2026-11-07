// SPDX-License-Identifier: AGPL-3.0-or-later

package template

import (
	"strings"
	"testing"
)

func mig(from, to string) Migration { return Migration{From: from, To: to} }

func TestUpgradePath(t *testing.T) {
	ms := []Migration{mig(">=1.0.0 <2.0.0", "2.0.0"), mig(">=2.0.0 <3.0.0", "3.0.0"), mig("=2.5.0", "2.6.0")}
	for _, tc := range []struct {
		from, to string
		want     string // the To of each step
		err      string
	}{
		{"1.2.0", "1.2.0", "", ""},
		{"1.2.0", "1.9.0", "", ""},            // nothing needed within a major
		{"1.2.0", "2.0.0", "2.0.0", ""},       // one step
		{"1.2.0", "2.4.0", "2.0.0", ""},       // and a plain upgrade after it
		{"1.2.0", "3.1.0", "2.0.0 3.0.0", ""}, // chained
		{"2.5.0", "2.7.0", "2.6.0", ""},       // a minor can need one too
		{"3.0.0", "2.0.0", "", "goes down"},
		{"3.0.0", "4.0.0", "", "no migration path"},
		{"0.9.0", "1.0.0", "", "no migration path"},
	} {
		got, err := UpgradePath(ms, tc.from, tc.to)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s → %s: %v", tc.from, tc.to, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s → %s: %v", tc.from, tc.to, err)
			continue
		}
		var tos []string
		for _, m := range got {
			tos = append(tos, m.To)
		}
		if strings.Join(tos, " ") != tc.want {
			t.Errorf("%s → %s: %v", tc.from, tc.to, tos)
		}
	}
	// Below 1.0 a minor can break.
	if _, err := UpgradePath(nil, "0.1.0", "0.2.0"); err == nil {
		t.Error("0.1 → 0.2 without a migration")
	}
	if _, err := UpgradePath(nil, "0.1.0", "0.1.5"); err != nil {
		t.Error(err)
	}
	// A dead end is backed out of.
	got, err := UpgradePath([]Migration{mig("=1.0.0", "1.5.0"), mig("=1.0.0", "1.2.0"), mig("=1.2.0", "2.0.0")}, "1.0.0", "2.0.0")
	if err != nil || len(got) != 2 {
		t.Errorf("%v %v", got, err)
	}
	// Migrations can't loop: going up is the only way.
	if _, err := UpgradePath([]Migration{mig(">=1.0.0", "1.0.0")}, "1.0.0", "1.1.0"); err != nil {
		t.Error(err)
	}
	if _, err := UpgradePath([]Migration{mig("nonsense", "2.0.0")}, "1.0.0", "2.0.0"); err == nil {
		t.Error("a bad range must not pass")
	}
}

func TestPreReleaseOrder(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0-rc.2", "1.0.0-rc.10", -1},
		{"1.0.0-1", "1.0.0-a", -1},
		{"1.0.0-rc", "1.0.0-rc.1", -1},
		{"1.0.1", "1.0.0", 1},
	} {
		a, _ := parseSemver(tc.a)
		b, _ := parseSemver(tc.b)
		if a.compare(b) != tc.want {
			t.Errorf("%s vs %s", tc.a, tc.b)
		}
	}
}

func TestCheckMigration(t *testing.T) {
	run := Step{Service: "db", Run: []string{"x"}}
	dep := Step{Deploy: true, Wait: "healthy"}
	good := Migration{From: ">=1.0.0", To: "2.0.0", Steps: []Step{run, dep, run}}
	if bad := checkMigration(good); len(bad) != 0 {
		t.Errorf("%v", bad)
	}
	for name, m := range map[string]Migration{
		"bad range":   {From: "soon", Steps: []Step{run}},
		"two deploys": {From: "=1.0.0", Steps: []Step{dep, dep}},
		"no service":  {From: "=1.0.0", Steps: []Step{{Run: []string{"x"}}}},
		"empty step":  {From: "=1.0.0", Steps: []Step{{Name: "x"}}},
		"both":        {From: "=1.0.0", Steps: []Step{{Deploy: true, Service: "db"}}},
		"wait on run": {From: "=1.0.0", Steps: []Step{{Service: "db", Run: []string{"x"}, Wait: "healthy"}}},
	} {
		if len(checkMigration(m)) == 0 {
			t.Errorf("%s must be refused", name)
		}
	}
}
