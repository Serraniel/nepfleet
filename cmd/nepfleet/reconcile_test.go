// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/deploy"
)

// removeTestProject removes the containers and networks a test started:
// only those whose compose working directory is in the test's state.
func removeTestProject(t *testing.T, stateDir string) {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label=com.docker.compose.project.working_dir").Output()
	if err != nil {
		return
	}
	for _, id := range strings.Fields(string(out)) {
		wd, err := exec.Command("docker", "inspect", "-f", `{{index .Config.Labels "com.docker.compose.project.working_dir"}}`, id).Output()
		if err != nil || !strings.HasPrefix(strings.TrimSpace(string(wd)), stateDir) {
			continue
		}
		project, _ := exec.Command("docker", "inspect", "-f", `{{index .Config.Labels "com.docker.compose.project"}}`, id).Output()
		_ = exec.Command("docker", "rm", "-fv", id).Run()
		if p := strings.TrimSpace(string(project)); p != "" {
			_ = exec.Command("docker", "network", "rm", p+"_default").Run()
		}
	}
}

func TestNeedsPerson(t *testing.T) {
	if r := needsPerson(&deploy.Plan{}); r != "" {
		t.Errorf("plain: %q", r)
	}
	if r := needsPerson(&deploy.Plan{Destructive: []string{"removes volume data"}}); !strings.Contains(r, "destructive") {
		t.Errorf("destructive: %q", r)
	}
	if r := needsPerson(&deploy.Plan{MigrationError: "no migration path"}); r != "no migration path" {
		t.Errorf("migration error: %q", r)
	}
	if r := needsPerson(&deploy.Plan{Migration: &deploy.PlannedMigration{From: "1.0.0", To: "2.0.0"}}); !strings.Contains(r, "2.0.0") {
		t.Errorf("migration: %q", r)
	}
	a := planKey(&deploy.Plan{Changes: []deploy.Change{{Path: ".env", Kind: "changed", Diff: "+A=1"}}})
	b := planKey(&deploy.Plan{Changes: []deploy.Change{{Path: ".env", Kind: "changed", Diff: "+A=2"}}})
	if a == b || a != planKey(&deploy.Plan{Changes: []deploy.Change{{Path: ".env", Kind: "changed", Diff: "+A=1"}}}) {
		t.Error("planKey must follow the changes")
	}
}

// An auto stack whose files changed without a commit is applied; a manual
// one isn't; a stack never applied is left to a person.
func TestReconcile(t *testing.T) {
	c, dir, _ := rollbackFleet(t)
	stackFile := filepath.Join(dir, "stacks", "web", "stack.yaml")
	setApply := func(mode string) {
		t.Helper()
		y := "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: web}\nspec: {target: local, apply: " + mode + "}\n"
		if err := os.WriteFile(stackFile, []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var log bytes.Buffer
	r := newReconciler(c, reconcileEvery, &log)
	ctx := context.Background()

	// Never applied: nothing happens, even on auto.
	setApply("auto")
	if !r.once(ctx) {
		t.Fatal("busy")
	}
	if strings.Contains(log.String(), "applying web") {
		t.Fatalf("a stack never applied was applied:\n%s", log.String())
	}

	// Applied once by a person, then the compose file changes.
	t.Cleanup(func() { removeTestProject(t, c.stateDir) })
	if err := runApply(ctx, c, []string{"web"}, applyOptions{yes: true, wait: applyWait}, &log, &log); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	compose := filepath.Join(dir, "stacks", "web", "compose.yaml")
	if err := os.WriteFile(compose, []byte("services:\n  web:\n    image: nginx:1.29-alpine\n    environment: {GREETING: hi}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	setApply("manual")
	log.Reset()
	r.once(ctx)
	if strings.Contains(log.String(), "applying web") {
		t.Fatalf("a manual stack was applied:\n%s", log.String())
	}

	setApply("auto")
	log.Reset()
	r.once(ctx)
	if !strings.Contains(log.String(), "applying web") || !strings.Contains(log.String(), "applied and healthy") {
		t.Fatalf("auto stack not applied:\n%s", log.String())
	}
	// Nothing left: the next round does nothing.
	log.Reset()
	r.once(ctx)
	if strings.Contains(log.String(), "applying") {
		t.Errorf("applied twice:\n%s", log.String())
	}
}
