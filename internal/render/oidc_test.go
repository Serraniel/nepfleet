// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

func TestOIDCBuiltins(t *testing.T) {
	f, tpl := setup(t)
	write(t, filepath.Join(tpl.Dir, "compose.yaml"), "services:\n  web:\n    image: nginx:1.29\n    environment: {D: '${DOMAIN}', ON: '${NEPFLEET_OIDC_ENABLED}', I: '${NEPFLEET_OIDC_ISSUER}',\n"+
		"      C: '${NEPFLEET_OIDC_CLIENT_ID}', S: '${NEPFLEET_OIDC_CLIENT_SECRET}', SC: '${NEPFLEET_OIDC_SCOPES}'}\n")
	tpl.Spec.OIDC = &template.OIDC{Redirect: "/cb", Scopes: []string{"openid", "groups"}}
	s := stack(map[string]string{"DOMAIN": "app.example.test"})

	// Without sign-on: off and empty, never missing.
	out, err := Render(Input{Fleet: f, Stack: s, Template: tpl})
	must(t, err)
	env := EnvValues(out.Files[EnvFile])
	if env[template.OIDCEnabled] != "false" || env[template.OIDCClientID] != "" || env[template.OIDCScopes] != "" {
		t.Errorf("no sign-on: %v", env)
	}

	secret := "kept-client-secret-0123456789"
	out, err = Render(Input{Fleet: f, Stack: s, Template: tpl, OIDC: &OIDC{Issuer: "https://id.example.test", ClientID: "myapp", ClientSecret: secret}})
	must(t, err)
	env = EnvValues(out.Files[EnvFile])
	if env[template.OIDCEnabled] != "true" || env[template.OIDCIssuer] != "https://id.example.test" || env[template.OIDCClientID] != "myapp" ||
		env[template.OIDCClientSecret] != secret || env[template.OIDCScopes] != "openid groups" {
		t.Errorf("sign-on: %v", env)
	}
	if strings.Contains(string(out.Masked(EnvFile)), secret) {
		t.Error("the client secret shows in the plan")
	}
	if len(out.Generated) != 1 { // KEY only: the client secret is stored already
		t.Errorf("generated %v", out.Generated)
	}

	out, err = Render(Input{Fleet: f, Stack: s, Template: tpl, OIDC: &OIDC{Issuer: "https://id.example.test", ClientID: "myapp", ClientSecret: secret, SecretNew: true}})
	must(t, err)
	if out.Generated[template.OIDCClientSecret] != secret {
		t.Errorf("a new client secret isn't stored: %v", out.Generated)
	}

	// Like every built-in, .env holds only the ones the files use.
	write(t, filepath.Join(tpl.Dir, "compose.yaml"), "services:\n  web:\n    image: nginx:1.29\n    environment: {D: '${DOMAIN}', C: '${NEPFLEET_OIDC_CLIENT_ID}'}\n")
	out, err = Render(Input{Fleet: f, Stack: s, Template: tpl, OIDC: &OIDC{Issuer: "https://id.example.test", ClientID: "myapp", ClientSecret: secret}})
	must(t, err)
	env = EnvValues(out.Files[EnvFile])
	if env[template.OIDCClientID] != "myapp" {
		t.Errorf("used built-in missing: %v", env)
	}
	for _, k := range []string{template.OIDCEnabled, template.OIDCIssuer, template.OIDCClientSecret, template.OIDCScopes, "NEPFLEET_SMTP_HOST"} {
		if _, ok := env[k]; ok {
			t.Errorf("%s written though nothing uses it", k)
		}
	}
}

func TestStackURLHelper(t *testing.T) {
	s := &fleet.Stack{}
	if got := StackURL(s, nil, "id.example.test"); got != "https://id.example.test" {
		t.Errorf("domain: %q", got)
	}
	s.Spec.Target = "box"
	s.Spec.Access = &fleet.Access{Mode: "port", Port: 8443}
	h := &fleet.Host{}
	h.Spec.Address = "10.0.0.5"
	if got := StackURL(s, h, ""); got != "http://10.0.0.5:8443" {
		t.Errorf("port mode: %q", got)
	}
}
