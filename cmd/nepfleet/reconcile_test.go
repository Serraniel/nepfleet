// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/api"
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

// After a NepFleet update renders an auto stack differently, with nothing
// changed in the fleet repository, the next round applies it. Built-ins the
// stack doesn't use are no change at all.
func TestReconcileRenderChange(t *testing.T) {
	c, dir, _ := rollbackFleet(t)
	st := filepath.Join(dir, "stacks", "web")
	for name, y := range map[string]string{
		"stack.yaml":   "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: web}\nspec: {target: local, apply: auto}\n",
		"compose.yaml": "services:\n  web:\n    image: nginx:1.29-alpine\n    environment: {STACK: '${NEPFLEET_STACK}'}\n",
	} {
		if err := os.WriteFile(filepath.Join(st, name), []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tgit(t, dir, "commit", "-qam", "auto", "--no-verify")
	var log bytes.Buffer
	ctx := context.Background()
	t.Cleanup(func() { removeTestProject(t, c.stateDir) })
	if err := runApply(ctx, c, []string{"web"}, applyOptions{yes: true, wait: applyWait}, &log, &log); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	envFile := filepath.Join(deploy.StackDir(c.stateDir, "web"), ".env")
	rendered, err := os.ReadFile(envFile) //nolint:gosec // the test's state
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), "NEPFLEET_API_URL") {
		t.Fatalf("an unused built-in was written:\n%s", rendered)
	}
	r := newReconciler(c, reconcileEvery, &log)

	// An older NepFleet wrote every built-in: not a change.
	if err := os.WriteFile(envFile, append(rendered, "NEPFLEET_API_URL='http://nepfleet:7480'\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	log.Reset()
	r.once(ctx)
	if strings.Contains(log.String(), "applying web") {
		t.Fatalf("unused built-ins were applied:\n%s", log.String())
	}

	// It rendered a used one differently: applied.
	old := strings.Replace(string(rendered), "NEPFLEET_STACK='web'", "NEPFLEET_STACK='old'", 1)
	if old == string(rendered) {
		t.Fatalf("NEPFLEET_STACK not in .env:\n%s", rendered)
	}
	if err := os.WriteFile(envFile, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	log.Reset()
	r.once(ctx)
	if !strings.Contains(log.String(), "applying web") || !strings.Contains(log.String(), "applied and healthy") {
		t.Fatalf("a render change was not applied:\n%s", log.String())
	}
	if now, _ := os.ReadFile(envFile); string(now) != string(rendered) { //nolint:gosec // the test's state
		t.Errorf(".env after apply:\n%s", now)
	}
}

func TestSetTemplateVersion(t *testing.T) {
	raw := "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: web}\nspec:\n  target: local\n  # which template\n  template:\n    source: official\n    name: caddy\n    version: 0.2.0\n"
	out, err := setTemplateVersion([]byte(raw), "0.3.1")
	if err != nil || !strings.Contains(string(out), "version: 0.3.1") || !strings.Contains(string(out), "# which template") || strings.Contains(string(out), "0.2.0") {
		t.Errorf("%s %v", out, err)
	}
}

// An auto stack moves to a newer template of the same major by itself, and
// is applied with it; a new major version is only announced.
func TestReconcileTemplateUpdate(t *testing.T) {
	c, dir := newStackFleet(t)
	t.Cleanup(func() { removeTestProject(t, c.stateDir) })
	if _, err := newStackAPI(c, api.NewStack{Template: "hello", Source: "local", Name: "greeter", Values: map[string]string{"GREETING": "hi"}}, "Ada"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var log bytes.Buffer
	if err := runApply(ctx, c, []string{"greeter"}, applyOptions{yes: true, wait: applyWait}, &log, &log); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	tgit(t, dir, "add", "-A")
	tgit(t, dir, "commit", "-qm", "applied", "--no-verify", "--allow-empty")

	// The template source gets 0.2.0, which adds a value.
	e, err := c.open()
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.fleet.LoadStack("greeter")
	if err != nil {
		t.Fatal(err)
	}
	src := ""
	for _, ts := range e.fleet.Spec.TemplateSources {
		if ts.Name == "local" {
			src = ts.Path
		}
	}
	bump := func(version, compose string) {
		t.Helper()
		tplFile := filepath.Join(src, "apps", "hello", "template.yaml")
		raw, err := os.ReadFile(tplFile) //nolint:gosec // the test's template
		if err != nil {
			t.Fatal(err)
		}
		out := regexp.MustCompile(`(?m)^  version: .*$`).ReplaceAllString(string(raw), "  version: "+version)
		if err := os.WriteFile(tplFile, []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "apps", "hello", "compose.yaml"), []byte(compose), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bump("0.2.0", "services:\n  web:\n    image: nginx:1.29-alpine\n    environment:\n      GREETING: ${GREETING}\n      LEVEL: ${LEVEL}\n      NEW: \"yes\"\n")
	if s.Spec.Template.Version != "0.1.0" {
		t.Fatalf("pin: %s", s.Spec.Template.Version)
	}

	r := newReconciler(c, reconcileEvery, &log)
	log.Reset()
	r.once(ctx)
	if !strings.Contains(log.String(), "template hello 0.1.0 → 0.2.0") || !strings.Contains(log.String(), "applied and healthy") {
		t.Fatalf("not moved and applied:\n%s", log.String())
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "stacks", "greeter", "stack.yaml")) //nolint:gosec // the test's fleet
	if !strings.Contains(string(raw), "version: 0.2.0") {
		t.Errorf("stack.yaml: %s", raw)
	}
	if got := tgit(t, dir, "log", "--format=%an|%s", "-3"); !strings.Contains(got, "nepfleet|chore(greeter): template hello 0.1.0 → 0.2.0") {
		t.Errorf("commits:\n%s", got)
	}

	// 1.0.0: announced, not moved.
	bump("1.0.0", "services:\n  web:\n    image: nginx:1.29-alpine\n")
	log.Reset()
	r.once(ctx)
	if strings.Contains(log.String(), "→ 1.0.0") || !strings.Contains(log.String(), "a new major version") {
		t.Errorf("major:\n%s", log.String())
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "stacks", "greeter", "stack.yaml")) //nolint:gosec // the test's fleet
	if !strings.Contains(string(raw), "version: 0.2.0") {
		t.Errorf("moved across a major: %s", raw)
	}
}
