// SPDX-License-Identifier: AGPL-3.0-or-later

package template

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// OIDC says how a template signs in through OpenID Connect, so NepFleet can
// register a client for each stack in the fleet's identity provider
// (docs/sign-on.md).
type OIDC struct {
	// Redirect is the app's callback path, appended to the stack's URL. It
	// may use inputs, as ${NAME}.
	Redirect string `yaml:"redirect" schema:"required;pattern=^/" json:"redirect"`
	// Scopes the app asks for; default openid, profile and email.
	Scopes []string `yaml:"scopes" json:"scopes,omitempty"`
	// Logout is the path the provider may send users to after signing out.
	Logout string `yaml:"logout" schema:"pattern=^/" json:"logout,omitempty"`
}

// DefaultScopes are the scopes of a template that names none.
var DefaultScopes = []string{"openid", "profile", "email"}

// ScopeList is the scopes the app asks for.
func (o *OIDC) ScopeList() []string {
	if len(o.Scopes) == 0 {
		return DefaultScopes
	}
	return o.Scopes
}

// The sign-on built-ins: filled when a stack signs in through the fleet's
// provider or a provider of its own, empty (ENABLED false) otherwise.
const (
	OIDCEnabled      = "NEPFLEET_OIDC_ENABLED"
	OIDCIssuer       = "NEPFLEET_OIDC_ISSUER"
	OIDCClientID     = "NEPFLEET_OIDC_CLIENT_ID"
	OIDCClientSecret = "NEPFLEET_OIDC_CLIENT_SECRET" //nolint:gosec // a variable's name, not a secret
	OIDCScopes       = "NEPFLEET_OIDC_SCOPES"
)

// OIDCBuiltins are the sign-on built-ins, which only templates with
// spec.oidc may use.
var OIDCBuiltins = []string{OIDCEnabled, OIDCIssuer, OIDCClientID, OIDCClientSecret, OIDCScopes}

// Kept next to the rest of sign-on: Builtins lists every built-in.
func init() {
	Builtins = append(Builtins, OIDCBuiltins...)
}

// scopeRe is an RFC 6749 scope token.
var scopeRe = regexp.MustCompile(`^[\x21\x23-\x5B\x5D-\x7E]+$`)

// checkOIDC validates spec.oidc, and that the sign-on built-ins are used
// exactly by templates that declare it.
func (t *Template) checkOIDC(declared map[string]bool) []error {
	var errs []error
	o := t.Spec.OIDC
	if o != nil {
		for _, p := range []struct{ field, v string }{{"redirect", o.Redirect}, {"logout", o.Logout}} {
			if p.v == "" && p.field == "logout" {
				continue
			}
			if !callbackPath(p.v) {
				errs = append(errs, fmt.Errorf("spec.oidc.%s: %q must be a path starting with /", p.field, p.v))
			}
			for _, m := range composeRe.FindAllStringSubmatch(p.v, -1) {
				if !declared[m[1]] || slices.Contains(OIDCBuiltins, m[1]) {
					errs = append(errs, fmt.Errorf("spec.oidc.%s: ${%s} is not an input", p.field, m[1]))
				}
			}
		}
		for _, s := range o.Scopes {
			if !scopeRe.MatchString(s) {
				errs = append(errs, fmt.Errorf("spec.oidc.scopes: %q is not a scope", s))
			}
		}
		if len(o.Scopes) > 0 && !slices.Contains(o.Scopes, "openid") {
			errs = append(errs, errors.New("spec.oidc.scopes: OpenID Connect needs openid"))
		}
		if !t.Spec.Supports.SSO {
			errs = append(errs, errors.New("spec.oidc needs supports.sso: true"))
		}
	}
	if t.Dir == "" {
		return errs
	}
	used := map[string]bool{}
	for _, name := range t.renderedSources() {
		raw, err := os.ReadFile(filepath.Join(t.Dir, name))
		if err != nil {
			continue // reported by the compose and files checks
		}
		for _, b := range OIDCBuiltins {
			if bytes.Contains(raw, []byte("${"+b)) {
				used[b] = true
			}
		}
	}
	switch {
	case o == nil && len(used) > 0:
		errs = append(errs, errors.New("the sign-on built-ins (NEPFLEET_OIDC_*) need spec.oidc"))
	case o != nil && !used[OIDCClientID]:
		errs = append(errs, fmt.Errorf("spec.oidc: nothing uses ${%s}", OIDCClientID))
	}
	return errs
}

// renderedSources are the template's files that NepFleet renders with its
// variables: the compose file and spec.files.
func (t *Template) renderedSources() []string {
	out := []string{}
	if local(t.Spec.Compose) {
		out = append(out, t.Spec.Compose)
	}
	for _, f := range t.Spec.Files {
		if local(f.Src) {
			out = append(out, f.Src)
		}
	}
	return out
}

// callbackPath: an absolute URL path, without host, query or fragment.
func callbackPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, " \t\n?#") {
		return false
	}
	u, err := url.Parse(p)
	return err == nil && u.Host == "" && u.Scheme == ""
}
