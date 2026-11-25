// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/render"
	"github.com/Serraniel/nepfleet/internal/signon"
	"github.com/Serraniel/nepfleet/internal/signon/signontest"
	"github.com/Serraniel/nepfleet/internal/template"
)

// signOnFleet is a fleet with a Pocket ID stack "id" on a LAN port (the
// fake's) and a stack "wiki" whose template declares spec.oidc.
func signOnFleet(t *testing.T, port string) (common, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "f")
	if err := run([]string{"init", dir}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	files := map[string]string{
		"apps/wiki/template.yaml": `apiVersion: nepfleet/v1alpha1
kind: Template
metadata: {name: wiki, title: Wiki, description: A wiki., tier: app}
spec:
  version: 1.0.0
  compose: compose.yaml
  images: [{name: nginx, track: minor}]
  supports: {sso: true}
  oidc: {redirect: "/auth/${LOGIN}/callback", logout: /}
  inputs:
    - {name: DOMAIN, type: hostname, required: true}
    - {name: LOGIN, type: string, default: sso}
`,
		"apps/wiki/compose.yaml": "services:\n  web:\n    image: nginx:1.29-alpine\n    environment:\n      HOST: ${DOMAIN}\n" +
			"      OIDC: ${NEPFLEET_OIDC_ENABLED}\n      ISSUER: ${NEPFLEET_OIDC_ISSUER}\n      ID: ${NEPFLEET_OIDC_CLIENT_ID}\n      SECRET: ${NEPFLEET_OIDC_CLIENT_SECRET}\n",
		"system/pocket-id/template.yaml": `apiVersion: nepfleet/v1alpha1
kind: Template
metadata: {name: pocket-id, title: Pocket ID, description: OIDC., tier: system}
spec:
  version: 1.0.0
  compose: compose.yaml
  web: {service: pocket-id, port: 1411}
  images: [{name: ghcr.io/pocket-id/pocket-id, track: minor}]
  inputs:
    - {name: DOMAIN, type: hostname, required: true}
  generate:
    - {name: STATIC_API_KEY, kind: secret, length: 32}
`,
		"system/pocket-id/compose.yaml": "services:\n  pocket-id:\n    image: ghcr.io/pocket-id/pocket-id:v2.19.3\n    environment: {KEY: '${STATIC_API_KEY}', D: '${DOMAIN}'}\n",
	}
	for name, content := range files {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stacks := map[string]string{
		"system/id":   "  template: {source: local, name: pocket-id}\n  access: {mode: port, port: " + port + "}\n  values: {DOMAIN: id.example.test}\n",
		"stacks/wiki": "  template: {source: local, name: wiki}\n  values: {DOMAIN: wiki.example.test}\n",
	}
	for path, spec := range stacks {
		d := filepath.Join(dir, path)
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
		y := "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: " + filepath.Base(path) + "}\nspec:\n  target: local\n" + spec
		if err := os.WriteFile(filepath.Join(d, "stack.yaml"), []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setFleet(t, dir, "  templateSources:\n", "  templateSources:\n    - name: local\n      path: "+src+"\n")
	// The host's address is its name: 127.0.0.1, where the fake listens.
	return common{fleetDir: dir, stateDir: t.TempDir(), host: "127.0.0.1"}, dir
}

func setFleet(t *testing.T, dir, old, repl string) {
	t.Helper()
	fy := filepath.Join(dir, "fleet.yaml")
	raw, err := os.ReadFile(fy) //nolint:gosec // the test's fleet
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), old) {
		t.Fatalf("fleet.yaml lacks %q", old)
	}
	if err := os.WriteFile(fy, []byte(strings.Replace(string(raw), old, repl, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setStack(t *testing.T, dir, stack, old, repl string) {
	t.Helper()
	p := filepath.Join(dir, "stacks", stack, "stack.yaml")
	raw, err := os.ReadFile(p) //nolint:gosec // the test's fleet
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(raw), old, repl, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIntegratedSignOn(t *testing.T) {
	const key = "static-api-key-0123456789"
	srv := signontest.New(key)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c, dir := signOnFleet(t, u.Port())
	ctx := context.Background()

	prep := func() *prepared {
		t.Helper()
		e, err := c.open()
		if err != nil {
			t.Fatal(err)
		}
		p, err := e.prepare(mustStack(t, e, "wiki"))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}

	// No provider in the fleet: sign-on is off, nothing to register.
	p := prep()
	if env := render.EnvValues(p.out.Files[render.EnvFile]); env[template.OIDCEnabled] != "false" || p.signOn != nil {
		t.Fatalf("without a provider: %v %+v", env, p.signOn)
	}

	setFleet(t, dir, "spec:\n", "spec:\n  signOn: {provider: id}\n")
	p = prep()
	env := render.EnvValues(p.out.Files[render.EnvFile])
	secret := env[template.OIDCClientSecret]
	if env[template.OIDCEnabled] != "true" || env[template.OIDCIssuer] != srv.URL || env[template.OIDCClientID] != "wiki" || len(secret) != 48 {
		t.Fatalf("integrated: %v", env)
	}
	if p.out.Generated[template.OIDCClientSecret] != secret {
		t.Error("the new client secret isn't stored on apply")
	}
	if got := strings.Join(p.signOn.client.Callbacks, ","); got != "https://wiki.example.test/auth/sso/callback" {
		t.Errorf("callbacks %s", got)
	}

	// The provider's API key isn't there yet (it is never applied): the
	// plan fails with the reason, rather than going ahead.
	e, _ := c.open()
	if _, _, err := e.preparePlan(ctx, mustStack(t, e, "wiki"), io.Discard); err == nil || !strings.Contains(err.Error(), "STATIC_API_KEY is not set") {
		t.Errorf("provider without its key: %v", err)
	}

	// With the provider: the plan creates the client first, apply registers it.
	p.signOn.provider = &signon.PocketID{URL: srv.URL, APIKey: key}
	if err := e.planSignOn(ctx, p); err != nil {
		t.Fatal(err)
	}
	if !hasChange(p, "sign-on client wiki in id") || p.plan.Steps[0].Kind != "sign-on" {
		t.Fatalf("plan: %+v", p.plan)
	}
	if strings.Contains(p.plan.Changes[len(p.plan.Changes)-1].Diff, secret) {
		t.Error("the plan shows the client secret")
	}
	if err := ensureSignOn(ctx, p); err != nil {
		t.Fatal(err)
	}
	if !srv.Knows("wiki", secret) {
		t.Fatal("the client or its secret wasn't registered")
	}

	// Applied (the secret stored): the next plan is up to date.
	p = prep()
	p.signOn.provider = &signon.PocketID{URL: srv.URL, APIKey: key}
	p.signOn.client.Secret = secret
	if err := e.planSignOn(ctx, p); err != nil || hasChange(p, "sign-on client wiki in id") {
		t.Errorf("second plan: %v %+v", err, p.plan.Changes)
	}

	// A new domain moves the callback; nothing else changed, so only the
	// provider is touched.
	setStack(t, dir, "wiki", "wiki.example.test", "docs.example.test")
	p = prep()
	p.signOn.provider = &signon.PocketID{URL: srv.URL, APIKey: key}
	p.signOn.client.Secret = secret
	if err := e.planSignOn(ctx, p); err != nil || !hasChange(p, "sign-on client wiki in id") {
		t.Fatalf("domain change: %v %+v", err, p.plan.Changes)
	}
	p.plan.Changes = []deploy.Change{p.plan.Changes[len(p.plan.Changes)-1]}
	if !onlySignOn(p) {
		t.Error("a plan with only the client isn't seen as such")
	}
	if err := ensureSignOn(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := srv.Clients["wiki"]["callbackURLs"]; !strings.Contains(strings.Join(anyStrings(got), ","), "https://docs.example.test/auth/sso/callback") {
		t.Errorf("callback not moved: %v", got)
	}

	// The provider is down: the plan fails, the stack isn't applied.
	srv.Close()
	p = prep()
	p.signOn.provider = &signon.PocketID{URL: srv.URL, APIKey: key}
	if err := e.planSignOn(ctx, p); err == nil || !strings.Contains(err.Error(), "can't be asked") {
		t.Errorf("provider down: %v", err)
	}
}

func TestSignOnModes(t *testing.T) {
	c, dir := signOnFleet(t, "8443")
	prepErr := func() error {
		t.Helper()
		e, err := c.open()
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.prepare(mustStack(t, e, "wiki"))
		return err
	}
	setStack(t, dir, "wiki", "  values:", "  signOn: {mode: integrated}\n  values:")
	if err := prepErr(); err == nil || !strings.Contains(err.Error(), "needs a provider in fleet.yaml") {
		t.Errorf("integrated without a provider: %v", err)
	}
	setStack(t, dir, "wiki", "{mode: integrated}", "{mode: custom, issuer: https://login.example.test, clientID: wiki}")
	if err := prepErr(); err == nil || !strings.Contains(err.Error(), "nepfleet secret set wiki NEPFLEET_OIDC_CLIENT_SECRET") {
		t.Errorf("custom without its secret: %v", err)
	}
	setStack(t, dir, "wiki", "{mode: custom, issuer: https://login.example.test, clientID: wiki}", "{mode: none}")
	setFleet(t, dir, "spec:\n", "spec:\n  signOn: {provider: wiki}\n")
	if err := prepErr(); err != nil {
		t.Errorf("opted out: %v", err)
	}
	setStack(t, dir, "wiki", "{mode: none}", "{mode: integrated}")
	if err := prepErr(); err == nil || !strings.Contains(err.Error(), "can't sign in through itself") {
		t.Errorf("provider signing in through itself: %v", err)
	}
	setFleet(t, dir, "signOn: {provider: wiki}", "signOn: {provider: nope}")
	if err := prepErr(); err == nil || !strings.Contains(err.Error(), "spec.signOn.provider") {
		t.Errorf("missing provider stack: %v", err)
	}
}

func hasChange(p *prepared, path string) bool {
	for _, c := range p.plan.Changes {
		if c.Path == path {
			return true
		}
	}
	return false
}

func anyStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}
