// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFleetSignOn(t *testing.T) {
	for raw, ok := range map[string]bool{
		"{provider: pocket-id}": true,
		"{provider: Pocket}":    false,
		"{}":                    false,
	} {
		dir := t.TempDir()
		y := strings.Replace(skeleton["fleet.yaml"], "spec:\n", "spec:\n  signOn: "+raw+"\n", 1)
		if err := os.WriteFile(filepath.Join(dir, "fleet.yaml"), []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := Load(dir)
		if (err == nil) != ok {
			t.Errorf("%s: err = %v", raw, err)
		}
		if ok && f.Spec.SignOn.Provider != "pocket-id" {
			t.Errorf("%s: %+v", raw, f.Spec.SignOn)
		}
	}
}

func TestStackSignOn(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wiki")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  web:\n    image: nginx:1.29-alpine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	load := func(signOn string) (*Stack, error) {
		t.Helper()
		y := "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: wiki}\nspec:\n  target: local\n" + signOn
		if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
		return loadStack(dir)
	}
	for raw, want := range map[string]string{
		"":                                    "",
		"  signOn: {mode: none}\n":            "",
		"  signOn: {mode: integrated}\n":      "",
		"  signOn: {mode: sometimes}\n":       "integrated, custom or none",
		"  signOn: {mode: none, issuer: x}\n": "for mode custom",
		"  signOn: {mode: custom, issuer: https://id.example.test, clientID: wiki}\n": "",
		"  signOn: {mode: custom, issuer: id.example.test, clientID: wiki}\n":         "not an http(s) URL",
		"  signOn: {mode: custom, issuer: https://id.example.test}\n":                 "clientID is required",
	} {
		_, err := load(raw)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%q: err = %v, want %q", raw, err, want)
		}
	}

	f := &Fleet{}
	s, _ := load("")
	if got := SignOnMode(f, s, true); got != SignOnNone {
		t.Errorf("no provider: %s", got)
	}
	f.Spec.SignOn = &SignOnSpec{Provider: "id"}
	if got := SignOnMode(f, s, true); got != SignOnIntegrated {
		t.Errorf("provider and spec.oidc: %s", got)
	}
	if got := SignOnMode(f, s, false); got != SignOnNone {
		t.Errorf("template without spec.oidc: %s", got)
	}
	s, _ = load("  signOn: {mode: none}\n")
	if got := SignOnMode(f, s, true); got != SignOnNone {
		t.Errorf("opted out: %s", got)
	}
}
