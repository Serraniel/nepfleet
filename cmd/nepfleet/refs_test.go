// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

// refFleet creates a fleet with plain stacks; values are stack.yaml lines.
func refFleet(t *testing.T, age string, stacks map[string]string) *env {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "f")
	if err := run([]string{"init", dir}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if age != "" {
		path := filepath.Join(dir, "fleet.yaml")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(raw), "    age: []", "    age: [\""+age+"\"]", 1)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, values := range stacks {
		d := filepath.Join(dir, "stacks", name)
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
		y := "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: " + name + "}\nspec:\n  target: local\n  values:\n" + values
		if err := os.WriteFile(filepath.Join(d, "stack.yaml"), []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "compose.yaml"), []byte("services: {a: {image: \"nginx:1.29\"}}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e, err := common{fleetDir: dir, stateDir: t.TempDir(), host: "h"}.open()
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func resolve(t *testing.T, e *env, name string) (map[string]string, map[string]bool, error) {
	t.Helper()
	s, err := e.fleet.LoadStack(name)
	if err != nil {
		t.Fatal(err)
	}
	return e.resolveRefs(s)
}

func TestResolveValueChains(t *testing.T) {
	e := refFleet(t, "", map[string]string{
		"a":    "    URL: http://a:80\n",
		"b":    "    A_URL: {from: a, value: URL}\n",
		"c":    "    X: {from: b, value: A_URL}\n    MISSING: {from: a, value: NOPE}\n",
		"self": "    X: {from: self, value: Y}\n",
		"p":    "    X: {from: q, value: Y}\n",
		"q":    "    Y: {from: p, value: X}\n",
	})
	v, secret, err := resolve(t, e, "b")
	if err != nil || v["A_URL"] != "http://a:80" || secret["A_URL"] {
		t.Errorf("b: %v %v %v", v, secret, err)
	}
	if _, _, err := resolve(t, e, "c"); err == nil || !strings.Contains(err.Error(), "value NOPE of a is not set") {
		t.Errorf("missing value: %v", err)
	}
	if _, _, err := resolve(t, e, "self"); err == nil || !strings.Contains(err.Error(), "cannot take values from itself") {
		t.Errorf("self: %v", err)
	}
	if _, _, err := resolve(t, e, "p"); err == nil || !strings.Contains(err.Error(), "references loop: p → q → p") {
		t.Errorf("loop: %v", err)
	}
}

// A secret reached through value references stays a secret.
func TestResolveSecretChain(t *testing.T) {
	for _, tool := range []string{"sops", "age-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	key := filepath.Join(t.TempDir(), "key.txt")
	if out, err := exec.Command("age-keygen", "-o", key).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	pub, err := exec.Command("age-keygen", "-y", key).Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", key)
	e := refFleet(t, strings.TrimSpace(string(pub)), map[string]string{
		"a": "    PLAIN: x\n",
		"b": "    KEY: {from: a, secret: K}\n",
		"c": "    X: {from: b, value: KEY}\n",
	})
	a, _ := e.fleet.LoadStack("a")
	if err := e.store.Save(filepath.Join(a.Dir, fleet.SecretsFile), map[string]string{"K": "topsecret123"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b", "c"} {
		v, secret, err := resolve(t, e, name)
		if err != nil {
			t.Fatal(err)
		}
		for k, val := range v {
			if val != "topsecret123" || !secret[k] {
				t.Errorf("%s: %s=%q secret=%v", name, k, val, secret[k])
			}
		}
	}
	p, err := e.prepare(mustStack(t, e, "c"))
	if err != nil {
		t.Fatal(err)
	}
	if masked := string(p.out.Masked(".env")); strings.Contains(masked, "topsecret123") {
		t.Errorf("the secret shows in the plan:\n%s", masked)
	}
}

func mustStack(t *testing.T, e *env, name string) *fleet.Stack {
	t.Helper()
	s, err := e.fleet.LoadStack(name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
