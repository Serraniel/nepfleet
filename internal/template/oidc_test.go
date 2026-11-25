// SPDX-License-Identifier: AGPL-3.0-or-later

package template

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOIDCValidation(t *testing.T) {
	dir := copyDemo(t)
	compose := filepath.Join(dir, "compose.yaml")
	replaceIn(t, compose, "      KEY: ${SECRET_KEY}\n", "      KEY: ${SECRET_KEY}\n      OIDC_CLIENT: ${NEPFLEET_OIDC_CLIENT_ID}\n      OIDC_ON: ${NEPFLEET_OIDC_ENABLED}\n")
	tpl, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The built-ins without spec.oidc would always be empty.
	if err := tpl.Validate(); err == nil || !strings.Contains(err.Error(), "need spec.oidc") {
		t.Errorf("built-ins without spec.oidc: %v", err)
	}

	tpl.Spec.Supports.SSO = true
	tpl.Spec.OIDC = &OIDC{Redirect: "/auth/callback", Logout: "/", Scopes: []string{"openid", "email", "groups"}}
	if err := tpl.Validate(); err != nil {
		t.Fatalf("valid spec.oidc rejected: %v", err)
	}
	if got := strings.Join(tpl.Spec.OIDC.ScopeList(), " "); got != "openid email groups" {
		t.Errorf("scopes %q", got)
	}
	if got := strings.Join((&OIDC{}).ScopeList(), " "); got != "openid profile email" {
		t.Errorf("default scopes %q", got)
	}

	tpl.Spec.Supports.SSO = false
	tpl.Spec.OIDC = &OIDC{Redirect: "/auth/${DOMAIN}/cb"}
	if err := tpl.Validate(); err == nil || !strings.Contains(err.Error(), "supports.sso") || strings.Contains(err.Error(), "not an input") {
		t.Errorf("a redirect using an input: %v", err)
	}
	tpl.Spec.OIDC = &OIDC{Redirect: "https://evil.example.test/cb", Logout: "//${NOPE}", Scopes: []string{"profile", "bad scope"}}
	err = tpl.Validate()
	for _, want := range []string{"spec.oidc.redirect", "spec.oidc.logout", "${NOPE} is not an input", `"bad scope" is not a scope`, "needs openid", "supports.sso: true"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in %v", want, err)
		}
	}

	// spec.oidc that nothing uses.
	replaceIn(t, compose, "      OIDC_CLIENT: ${NEPFLEET_OIDC_CLIENT_ID}\n", "")
	tpl, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	tpl.Spec.Supports.SSO = true
	tpl.Spec.OIDC = &OIDC{Redirect: "/cb"}
	if err := tpl.Validate(); err == nil || !strings.Contains(err.Error(), "nothing uses ${NEPFLEET_OIDC_CLIENT_ID}") {
		t.Errorf("unused spec.oidc: %v", err)
	}
}
