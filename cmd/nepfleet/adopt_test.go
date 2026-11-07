// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const moveProject = "nepfleet-move-test"

func moveFleet(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	stack := filepath.Join(dir, "stacks", moveProject)
	if err := os.MkdirAll(stack, 0o750); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(dir, "fleet.yaml"):     "apiVersion: nepfleet/v1alpha1\nkind: Fleet\nmetadata:\n  name: test\nspec:\n  ingressNetwork: nepfleet-move-test-ingress\n",
		filepath.Join(stack, "stack.yaml"):   "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata:\n  name: " + moveProject + "\nspec:\n  target: local\n  mode: compose\n  apply: auto\n",
		filepath.Join(stack, "compose.yaml"): "services:\n  app:\n    image: alpine:3.22\n    stop_grace_period: 1s\n    command: [\"sleep\", \"600\"]\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A new installation takes over a running stack: nothing restarts, and the plan is
// empty. This is the runbook in docs/moving.md.
func TestMoveFleetToANewInstallation(t *testing.T) {
	if os.Getenv("NEPFLEET_TEST_DOCKER") == "" {
		t.Skip("set NEPFLEET_TEST_DOCKER to run with Docker")
	}
	fleetDir := moveFleet(t)
	oldState, newState := t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-p", moveProject, "--project-directory", filepath.Join(oldState, "stacks", moveProject), "down", "-v", "-t", "1").Run()
		_ = exec.Command("docker", "network", "rm", "nepfleet-move-test-ingress").Run()
	})
	var out, errs bytes.Buffer
	// Refused while nothing runs: there's nothing to take over.
	if err := cmdAdopt([]string{"--fleet", fleetDir, "--state", newState, moveProject}, &out, &errs); err == nil || !strings.Contains(err.Error(), "isn't running") {
		t.Fatalf("adopt with nothing running: %v", err)
	}
	if err := cmdApply([]string{"--yes", "--no-lock", "--no-snapshot", "--fleet", fleetDir, "--state", oldState, moveProject}, &out, &errs); err != nil {
		t.Fatalf("%v\n%s\n%s", err, out.String(), errs.String())
	}
	id := func() string {
		o, err := exec.Command("docker", "ps", "-q", "--no-trunc", "--filter", "label=com.docker.compose.project="+moveProject).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(o))
	}
	before := id()
	if before == "" {
		t.Fatal("nothing runs after apply")
	}
	out.Reset()
	if err := cmdAdopt([]string{"--fleet", fleetDir, "--state", newState, moveProject}, &out, &errs); err != nil {
		t.Fatalf("%v\n%s", err, errs.String())
	}
	if !strings.Contains(out.String(), "adopted") {
		t.Errorf("%s", out.String())
	}
	if id() != before {
		t.Error("adopting restarted the stack")
	}
	c := common{fleetDir: fleetDir, stateDir: newState, host: "test"}
	e, err := c.open()
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.fleet.LoadStack(moveProject)
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.prepare(s)
	if err != nil {
		t.Fatal(err)
	}
	if !p.plan.Empty() {
		t.Errorf("the plan should be empty after adopting: %+v", p.plan.Changes)
	}
	// Twice is harmless.
	out.Reset()
	if err := cmdAdopt([]string{"--fleet", fleetDir, "--state", newState, moveProject}, &out, &errs); err != nil || !strings.Contains(out.String(), "already managed") {
		t.Errorf("%v %s", err, out.String())
	}
}
