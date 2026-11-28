// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

func TestSignOnAPI(t *testing.T) {
	for _, tool := range []string{"sops", "age-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	c, dir := signOnFleet(t, "8443")
	key := filepath.Join(t.TempDir(), "key.txt")
	if out, err := exec.Command("age-keygen", "-o", key).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	pub, err := exec.Command("age-keygen", "-y", key).Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", key)
	setFleet(t, dir, "age: []", "age: ["+strings.TrimSpace(string(pub))+"]")
	// The API commits with the repository's config: never a signing prompt.
	for _, kv := range [][2]string{{"commit.gpgsign", "false"}, {"user.name", "Server"}, {"user.email", "server@example.test"}} {
		tgit(t, dir, "config", kv[0], kv[1])
	}
	tgit(t, dir, "add", "-A")
	tgit(t, dir, "commit", "-qm", "sign-on fleet")
	mode := func(s api.SignOnSettings, name string) string {
		for _, m := range s.Stacks {
			if m.Name == name {
				return m.Mode
			}
		}
		return "-"
	}

	s, err := signOnAPI(c)
	if err != nil || s.Provider != "" || !slices.Equal(s.Candidates, []string{"id"}) || !slices.Contains(s.Kinds, "pocket-id") || mode(s, "wiki") != "none" || mode(s, "id") != "-" {
		t.Fatalf("before: %+v %v", s, err)
	}
	// Integrated needs a provider first.
	if _, err := setStackSignOnAPI(c, "wiki", api.StackSignOnUpdate{Mode: "integrated"}, "ci"); !errors.As(err, new(api.InvalidError)) {
		t.Errorf("integrated without a provider: %v", err)
	}
	for _, p := range []string{"wiki", "nope"} {
		if _, err := setSignOnAPI(c, p, "ci"); !errors.As(err, new(api.InvalidError)) {
			t.Errorf("provider %s: %v", p, err)
		}
	}
	if _, err := setSignOnAPI(c, "id", "ci"); err != nil {
		t.Fatal(err)
	}
	if s, _ = signOnAPI(c); s.Provider != "id" || mode(s, "wiki") != "integrated" {
		t.Errorf("after naming id: %+v", s)
	}
	if _, err := setSignOnAPI(c, "id", "ci"); !errors.As(err, new(api.InvalidError)) {
		t.Errorf("the same provider again: %v", err)
	}

	// A stack's mode: checked, written, removed.
	if _, err := setStackSignOnAPI(c, "id", api.StackSignOnUpdate{Mode: "integrated"}, "ci"); !errors.As(err, new(api.InvalidError)) {
		t.Errorf("a template without oidc: %v", err)
	}
	if _, err := setStackSignOnAPI(c, "wiki", api.StackSignOnUpdate{Mode: "custom", Issuer: "https://login.example.test"}, "ci"); !errors.As(err, new(api.InvalidError)) {
		t.Errorf("custom without a client ID: %v", err)
	}
	custom := api.StackSignOnUpdate{Mode: "custom", Issuer: "https://login.example.test", ClientID: "wiki-app"}
	if _, err := setStackSignOnAPI(c, "wiki", custom, "ci"); err != nil {
		t.Fatal(err)
	}
	so, err := stackSignOnAPI(c, "wiki")
	if err != nil || !so.Supported || so.Mode != "custom" || so.Set != "custom" || so.ClientID != "wiki-app" || so.Provider != "id" || so.SecretSet == nil || *so.SecretSet {
		t.Errorf("custom: %+v %v", so, err)
	}
	if _, err := setStackSignOnAPI(c, "wiki", api.StackSignOnUpdate{}, "ci"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "stacks", "wiki", "stack.yaml"))
	if strings.Contains(string(raw), "signOn") {
		t.Errorf("default still has spec.signOn:\n%s", raw)
	}
	if so, _ = stackSignOnAPI(c, "wiki"); so.Mode != "integrated" || so.Set != "" {
		t.Errorf("default: %+v", so)
	}
	if _, err := stackSignOnAPI(c, "nope"); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("unknown stack: %v", err)
	}

	// A new stack with custom sign-on stores the secret encrypted.
	res, err := newStackAPI(c, api.NewStack{Template: "wiki", Source: "local", Name: "notes", Values: map[string]string{"DOMAIN": "notes.example.test"},
		SignOn: &api.NewStackSignOn{Mode: "custom", Issuer: "https://login.example.test", ClientID: "notes", ClientSecret: "s3cret-client-value"}}, "ci")
	if err != nil {
		t.Fatal(err)
	}
	e, _ := c.open()
	ns, err := e.fleet.LoadStack(res.Name)
	if err != nil || ns.Spec.SignOn == nil || ns.Spec.SignOn.Mode != fleet.SignOnCustom || ns.Spec.SignOn.ClientID != "notes" {
		t.Fatalf("new stack: %+v %v", ns, err)
	}
	sec, err := e.store.Load(filepath.Join(ns.Dir, fleet.SecretsFile))
	if err != nil || sec[template.OIDCClientSecret] != "s3cret-client-value" {
		t.Errorf("secret: %v", err)
	}
	if _, err := newStackAPI(c, api.NewStack{Template: "wiki", Source: "local", Name: "notes2", Values: map[string]string{"DOMAIN": "n2.example.test"},
		SignOn: &api.NewStackSignOn{Mode: "custom", Issuer: "https://login.example.test", ClientID: "n2"}}, "ci"); !errors.As(err, new(api.InvalidError)) {
		t.Errorf("custom without a secret: %v", err)
	}

	if _, err := removeSignOnAPI(c, "ci"); err != nil {
		t.Fatal(err)
	}
	if _, err := removeSignOnAPI(c, "ci"); !errors.Is(err, api.ErrNoSignOn) {
		t.Errorf("removing twice: %v", err)
	}
	if st := tgit(t, dir, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("left uncommitted: %s", st)
	}
}
