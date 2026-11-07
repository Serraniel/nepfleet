// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

func migratePrepared(t *testing.T, installed, version string, ms ...template.Migration) *prepared {
	t.Helper()
	dir := t.TempDir()
	if installed != "" {
		if err := os.MkdirAll(filepath.Join(dir, ".nepfleet"), 0o700); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(deploy.Manifest{Template: "app", TemplateVersion: installed})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".nepfleet", "manifest.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tpl := &template.Template{}
	tpl.Metadata.Name = "app"
	tpl.Spec.Version = version
	tpl.Spec.Migrations = ms
	return &prepared{tpl: tpl, stack: &fleet.Stack{}, plan: &deploy.Plan{Dir: dir, Mode: "compose"}}
}

func TestPlanMigration(t *testing.T) {
	step := template.Step{Name: "dump", Service: "db", Run: []string{"x"}}
	m := template.Migration{From: ">=1.0.0 <2.0.0", To: "2.0.0", Backup: "required", Rollback: "restore", Steps: []template.Step{step, {Deploy: true}}}

	pr := migratePrepared(t, "1.0.0", "2.0.0", m)
	(&env{}).planMigration(pr)
	if pr.plan.Migration == nil || !pr.dataSafe || len(pr.plan.Destructive) != 1 || pr.plan.Empty() {
		t.Fatalf("%+v", pr.plan)
	}
	if pr.migration(nil) == nil {
		t.Error("apply gets nothing to run")
	}
	var b bytes.Buffer
	printMigration(&b, pr.plan)
	if !strings.Contains(b.String(), "1.0.0 → 2.0.0") || !strings.Contains(b.String(), "dump (in db)") {
		t.Errorf("%s", b.String())
	}

	// A rollback has to get through, asking first.
	pr = migratePrepared(t, "2.0.0", "1.0.0", m)
	(&env{}).planMigration(pr)
	if pr.plan.MigrationError != "" || pr.plan.Migration != nil || len(pr.plan.Destructive) != 1 {
		t.Errorf("going back: %+v", pr.plan)
	}

	for name, pr := range map[string]*prepared{
		"no path": migratePrepared(t, "1.0.0", "3.0.0"),
		"several": migratePrepared(t, "1.0.0", "3.0.0", m, template.Migration{From: ">=2.0.0 <3.0.0", To: "3.0.0", Steps: []template.Step{step}}),
	} {
		(&env{}).planMigration(pr)
		if pr.plan.MigrationError == "" || pr.plan.Empty() {
			t.Errorf("%s must be refused: %+v", name, pr.plan)
		}
	}

	pr = migratePrepared(t, "1.0.0", "2.0.0", m)
	pr.plan.Mode = "swarm"
	(&env{}).planMigration(pr)
	if pr.plan.MigrationError == "" {
		t.Error("swarm must be refused")
	}

	pr = migratePrepared(t, "1.0.0", "2.0.0", m)
	(&env{noSnapshot: true}).planMigration(pr)
	if pr.plan.MigrationError == "" {
		t.Error("a required snapshot can't be turned off")
	}

	// Nothing says where it starts, or it's a first apply.
	pr = migratePrepared(t, "", "2.0.0", m)
	(&env{}).planMigration(pr)
	if pr.plan.Migration != nil || pr.plan.MigrationError != "" {
		t.Errorf("unknown start: %+v", pr.plan)
	}
}
