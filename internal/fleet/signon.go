// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"fmt"
	"net/url"
	"slices"
)

// SignOnSpec is fleet.yaml spec.signOn: the fleet's identity provider,
// which NepFleet registers an OpenID Connect client in for each stack
// whose template supports it (docs/sign-on.md).
type SignOnSpec struct {
	// Provider names the stack that runs the provider (a Pocket ID stack).
	Provider string `yaml:"provider" schema:"required;pattern=^[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?$"`
}

// SignOn is stack.yaml spec.signOn: how the stack's users sign in.
type SignOn struct {
	// Mode: integrated (a client in the fleet's provider, made by NepFleet),
	// custom (a provider of your own, Issuer and ClientID below, the secret
	// in values.secret.yaml as NEPFLEET_OIDC_CLIENT_SECRET) or none (the
	// app's own accounts). Default: integrated when the fleet has a provider
	// and the template supports it, else none.
	Mode string `yaml:"mode" schema:"enum=integrated|custom|none"`
	// Issuer is the provider's issuer URL (custom).
	Issuer string `yaml:"issuer"`
	// ClientID is the client's ID at the provider (custom).
	ClientID string `yaml:"clientID"`
}

// Sign-on modes.
const (
	SignOnIntegrated = "integrated"
	SignOnCustom     = "custom"
	SignOnNone       = "none"
)

// signOnModes are the modes Validate accepts; the schema tag lists the same.
var signOnModes = []string{SignOnIntegrated, SignOnCustom, SignOnNone}

func checkFleetSignOn(s *SignOnSpec) error {
	if s != nil && !nameRe.MatchString(s.Provider) {
		return fmt.Errorf("fleet.yaml: spec.signOn.provider %q must name a stack", s.Provider)
	}
	return nil
}

func checkSignOn(s *SignOn) error {
	if s == nil {
		return nil
	}
	if s.Mode != "" && !slices.Contains(signOnModes, s.Mode) {
		return fmt.Errorf("spec.signOn.mode must be integrated, custom or none")
	}
	switch s.Mode {
	case "", SignOnIntegrated, SignOnNone:
		if s.Issuer != "" || s.ClientID != "" {
			return fmt.Errorf("spec.signOn: issuer and clientID are for mode custom")
		}
	case SignOnCustom:
		u, err := url.Parse(s.Issuer)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("spec.signOn.issuer: %q is not an http(s) URL", s.Issuer)
		}
		if s.ClientID == "" {
			return fmt.Errorf("spec.signOn.clientID is required in mode custom")
		}
	}
	return nil
}

// SignOnMode is how a stack signs in: its own spec.signOn.mode, else
// integrated when the fleet has a provider and the template supports
// sign-on (oidc), else none.
func SignOnMode(f *Fleet, s *Stack, oidc bool) string {
	if s.Spec.SignOn != nil && s.Spec.SignOn.Mode != "" {
		return s.Spec.SignOn.Mode
	}
	if f != nil && f.Spec.SignOn != nil && oidc {
		return SignOnIntegrated
	}
	return SignOnNone
}
