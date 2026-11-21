// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

// A newer template asks for an admin e-mail and password: the stack lists them
// as missing, and one PUT sets both, the password encrypted.
func TestSetInputs(t *testing.T) {
	for _, tool := range []string{"sops", "age-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	c, dir := newStackFleet(t)
	// Secrets need an age key, as in secretFleet.
	key := filepath.Join(t.TempDir(), "key.txt")
	if out, err := exec.Command("age-keygen", "-o", key).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	pub, err := exec.Command("age-keygen", "-y", key).Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", key)
	fy := filepath.Join(dir, "fleet.yaml")
	fraw, err := os.ReadFile(fy) //nolint:gosec // the test's fleet
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fy, []byte(strings.Replace(string(fraw), "age: []", "age: ["+strings.TrimSpace(string(pub))+"]", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "commit", "-qam", "age", "--no-verify")
	if _, err := newStackAPI(c, api.NewStack{Template: "hello", Source: "local", Name: "greeter", Values: map[string]string{"GREETING": "hi"}}, "Ada"); err != nil {
		t.Fatal(err)
	}
	e, err := c.open()
	if err != nil {
		t.Fatal(err)
	}
	src := ""
	for _, ts := range e.fleet.Spec.TemplateSources {
		if ts.Name == "local" {
			src = ts.Path
		}
	}
	tplFile := filepath.Join(src, "apps", "hello", "template.yaml")
	raw, err := os.ReadFile(tplFile) //nolint:gosec // the test's template
	if err != nil {
		t.Fatal(err)
	}
	more := "    - name: ADMIN_EMAIL\n      type: email\n      title: Admin e-mail\n      required: true\n" +
		"    - name: ADMIN_PASSWORD\n      type: secret\n      title: Admin password\n      required: true\n      pattern: \"^.{8,}$\"\n"
	if err := os.WriteFile(tplFile, []byte(strings.Replace(string(raw), "    - name: LEVEL\n", more+"    - name: LEVEL\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}

	ins, err := stackInputsAPI(c, "greeter")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]api.StackInput{}
	for _, in := range ins {
		byName[in.Name] = in
	}
	if g := byName["GREETING"]; !g.Set || g.Missing || g.Value != "hi" {
		t.Errorf("GREETING: %+v", g)
	}
	if a, p := byName["ADMIN_EMAIL"], byName["ADMIN_PASSWORD"]; !a.Missing || !p.Missing || !p.Secret || a.Secret {
		t.Errorf("missing: %+v %+v", a, p)
	}
	if l := byName["LEVEL"]; l.Missing {
		t.Errorf("LEVEL has a default: %+v", l)
	}

	ctx := context.Background()
	var invalid api.InvalidError
	if _, err := setInputsAPI(ctx, c, "greeter", map[string]string{"ADMIN_EMAIL": "not-an-email", "ADMIN_PASSWORD": "long-enough"}, "Ada"); !errors.As(err, &invalid) || !strings.Contains(err.Error(), "ADMIN_EMAIL") || strings.Contains(err.Error(), "long-enough") {
		t.Errorf("bad e-mail: %v", err)
	}
	if _, err := setInputsAPI(ctx, c, "greeter", map[string]string{"ADMIN_PASSWORD": "short"}, "Ada"); !errors.As(err, &invalid) || strings.Contains(err.Error(), "short") {
		t.Errorf("short password (and never in the error): %v", err)
	}
	if _, err := setInputsAPI(ctx, c, "greeter", map[string]string{"NOPE": "x"}, "Ada"); !errors.As(err, &invalid) {
		t.Errorf("unknown input: %v", err)
	}
	commit, err := setInputsAPI(ctx, c, "greeter", map[string]string{"ADMIN_EMAIL": "admin@example.test", "ADMIN_PASSWORD": "hunter2-long", "GREETING": ""}, "Ada")
	if err != nil || commit == "" {
		t.Fatalf("set: %q %v", commit, err)
	}
	if got := tgit(t, dir, "log", "-1", "--format=%an|%s"); got != "Ada|chore(greeter): set ADMIN_EMAIL, ADMIN_PASSWORD" {
		t.Errorf("commit: %s", got)
	}
	if s := tgit(t, dir, "status", "--porcelain"); s != "" {
		t.Errorf("dirty: %s", s)
	}
	stack, _ := os.ReadFile(filepath.Join(dir, "stacks", "greeter", "stack.yaml")) //nolint:gosec // the test's fleet
	if !strings.Contains(string(stack), "ADMIN_EMAIL: \"admin@example.test\"") || strings.Contains(string(stack), "hunter2") || !strings.Contains(string(stack), "GREETING: hi") {
		t.Errorf("stack.yaml:\n%s", stack)
	}
	secrets, err := e.store.Load(filepath.Join(dir, "stacks", "greeter", fleet.SecretsFile))
	if err != nil || secrets["ADMIN_PASSWORD"] != "hunter2-long" {
		t.Errorf("secret not stored: %v", err)
	}
	ins, _ = stackInputsAPI(c, "greeter")
	for _, in := range ins {
		if in.Missing {
			t.Errorf("still missing: %s", in.Name)
		}
		if in.Name == "ADMIN_PASSWORD" && (in.Value != "" || !in.Set) {
			t.Errorf("a secret's value is never shown: %+v", in)
		}
	}
}
