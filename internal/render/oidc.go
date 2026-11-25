// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"strings"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

// OIDC is a stack's sign-on, for the NEPFLEET_OIDC_* built-ins.
type OIDC struct {
	Issuer, ClientID, ClientSecret string
	// SecretNew: ClientSecret was made for this apply; it is stored with
	// the generated secrets.
	SecretNew bool
}

// addOIDC fills the sign-on built-ins: empty, and ENABLED false, when the
// stack has no sign-on.
func addOIDC(in Input, env map[string]string, out *Output) {
	o := in.OIDC
	if o == nil {
		o = &OIDC{}
	}
	enabled := "false"
	scopes := ""
	if o.ClientID != "" {
		enabled = "true"
		if in.Template != nil && in.Template.Spec.OIDC != nil {
			scopes = strings.Join(in.Template.Spec.OIDC.ScopeList(), " ")
		}
	}
	env[template.OIDCEnabled] = enabled
	env[template.OIDCIssuer] = o.Issuer
	env[template.OIDCClientID] = o.ClientID
	env[template.OIDCClientSecret] = o.ClientSecret
	env[template.OIDCScopes] = scopes
	out.Secret[template.OIDCClientSecret] = true
	if o.SecretNew {
		out.Generated[template.OIDCClientSecret] = o.ClientSecret
	}
}

// StackURL is a stack's ${NEPFLEET_URL} from its domain (or its LAN port,
// on host h), without rendering it.
func StackURL(s *fleet.Stack, h *fleet.Host, domain string) string {
	return stackURL(Input{Stack: s, Host: h}, map[string]string{"DOMAIN": domain})
}
