// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/render"
	"github.com/Serraniel/nepfleet/internal/signon"
	"github.com/Serraniel/nepfleet/internal/template"
)

// Integrated sign-on (docs/sign-on.md): a stack whose template declares
// spec.oidc gets a client in the fleet's identity provider. Rendering needs
// only local files (the issuer is the provider stack's URL, the client
// secret is stored with the stack's secrets); planning asks the provider
// what is missing, and apply registers it before the stack starts. A
// provider that can't be asked fails the plan, so the stack is held with
// the reason instead of being applied against a client that may not exist.

// signOnPlanned is a stack's integrated sign-on: its client in the fleet's
// provider, and what apply changes there.
type signOnPlanned struct {
	// stack is the provider stack, issuer its URL.
	stack    *fleet.Stack
	issuer   string
	provider signon.Provider
	client   signon.Client
	change   signon.Change
}

// path is the plan's change entry for the provider's client.
func (s *signOnPlanned) path() string {
	return "sign-on client " + s.client.ID + " in " + s.stack.Metadata.Name
}

// providers make a Provider from the provider stack's issuer URL and
// secrets, by the provider stack's template.
var providers = map[string]func(issuer string, sec map[string]string) (signon.Provider, error){
	"pocket-id": func(issuer string, sec map[string]string) (signon.Provider, error) {
		key := sec["STATIC_API_KEY"]
		if key == "" {
			return nil, fmt.Errorf("its STATIC_API_KEY is not set yet: apply it first")
		}
		return &signon.PocketID{URL: issuer, APIKey: key}, nil
	},
}

// signOn is the stack's sign-on for rendering, and for integrated sign-on
// the client to keep in the provider (callbacks filled in after rendering,
// by signOnCallbacks). Nothing here talks to the provider.
func (e *env) signOn(s *fleet.Stack, tpl *template.Template, sec map[string]string) (*render.OIDC, *signOnPlanned, error) {
	var spec *template.OIDC
	if tpl != nil {
		spec = tpl.Spec.OIDC
	}
	mode := fleet.SignOnMode(e.fleet, s, spec != nil)
	if mode != fleet.SignOnNone && spec == nil {
		return nil, nil, fmt.Errorf("spec.signOn: mode %s needs a template that supports sign-on (spec.oidc)", mode)
	}
	switch mode {
	case fleet.SignOnCustom:
		secret := sec[template.OIDCClientSecret]
		if secret == "" {
			return nil, nil, fmt.Errorf("spec.signOn: set the client secret with `nepfleet secret set %s %s`", s.Metadata.Name, template.OIDCClientSecret)
		}
		return &render.OIDC{Issuer: s.Spec.SignOn.Issuer, ClientID: s.Spec.SignOn.ClientID, ClientSecret: secret}, nil, nil
	case fleet.SignOnIntegrated:
	default:
		return nil, nil, nil
	}
	if e.fleet.Spec.SignOn == nil {
		return nil, nil, fmt.Errorf("spec.signOn: mode integrated needs a provider in fleet.yaml (spec.signOn.provider)")
	}
	name := e.fleet.Spec.SignOn.Provider
	if name == s.Metadata.Name {
		return nil, nil, fmt.Errorf("spec.signOn: %s is the fleet's provider and can't sign in through itself", name)
	}
	ps, err := e.fleet.LoadStack(name)
	if err != nil {
		return nil, nil, fmt.Errorf("fleet.yaml spec.signOn.provider: %w", err)
	}
	kind := ""
	if ps.Spec.Template != nil {
		kind = ps.Spec.Template.Name
	}
	if _, ok := providers[kind]; !ok {
		return nil, nil, fmt.Errorf("fleet.yaml spec.signOn.provider: %s runs %q, not a provider NepFleet can register clients in (pocket-id)", name, kind)
	}
	issuer, err := e.providerURL(ps)
	if err != nil {
		return nil, nil, fmt.Errorf("fleet.yaml spec.signOn.provider: %s: %w", name, err)
	}
	o := &render.OIDC{Issuer: issuer, ClientID: s.Metadata.Name, ClientSecret: sec[template.OIDCClientSecret]}
	if o.ClientSecret == "" {
		g := template.Generate{Name: template.OIDCClientSecret, Kind: "secret", Length: 48, Encoding: "alnum"}
		if o.ClientSecret, err = g.Value(); err != nil {
			return nil, nil, err
		}
		o.SecretNew = true
	}
	planned := &signOnPlanned{stack: ps, issuer: issuer, client: signon.Client{
		ID: s.Metadata.Name, Name: clientName(s.Metadata.Name), Secret: o.ClientSecret,
		Description: "Managed by NepFleet for the stack " + s.Metadata.Name + ": callbacks are set on every apply.",
	}}
	return o, planned, nil
}

// clientName is the client's name in the provider: the stack's, cut to
// the 50 characters Pocket ID takes.
func clientName(stack string) string {
	return stack[:min(len(stack), 50)]
}

// providerURL is the provider stack's ${NEPFLEET_URL}, from its stack.yaml
// alone: its issuer.
func (e *env) providerURL(ps *fleet.Stack) (string, error) {
	domain := ""
	if v, ok := ps.Spec.Values["DOMAIN"]; ok {
		domain = v.Literal
		if v.IsRef() {
			refs, _, err := e.resolveRefs(ps)
			if err != nil {
				return "", err
			}
			domain = refs["DOMAIN"]
		}
	}
	var host *fleet.Host
	if ps.Spec.Access != nil && ps.Spec.Access.Mode == "port" {
		h, err := e.fleet.LoadHost(e.targetHost(ps.Spec.Target))
		if err != nil {
			return "", err
		}
		host = h
	}
	u := render.StackURL(ps, host, domain)
	if u == "" {
		return "", fmt.Errorf("it has no DOMAIN, so it has no URL")
	}
	return u, nil
}

// signOnCallbacks fills in the client's callbacks from the rendered stack:
// its URL with the template's paths.
func signOnCallbacks(p *prepared) error {
	so := p.signOn
	if so == nil {
		return nil
	}
	env := render.EnvValues(p.out.Files[render.EnvFile])
	maps.Copy(env, p.out.UnusedBuiltins)
	base := strings.TrimSuffix(env["NEPFLEET_URL"], "/")
	if base == "" {
		return fmt.Errorf("sign-on: the stack has no URL (DOMAIN) for its callback")
	}
	// Paths may use inputs (a provider name in the path, as Forgejo's).
	path := func(p string) string { return os.Expand(p, func(k string) string { return env[k] }) }
	spec := p.tpl.Spec.OIDC
	so.client.Callbacks = []string{base + path(spec.Redirect)}
	so.client.LogoutCallbacks = nil
	if spec.Logout != "" {
		so.client.LogoutCallbacks = []string{base + path(spec.Logout)}
	}
	return nil
}

// planSignOn asks the provider what the stack's client lacks and adds that
// to the plan. Any error means the provider can't be asked: the plan fails.
func (e *env) planSignOn(ctx context.Context, p *prepared) error {
	so := p.signOn
	if so == nil {
		return nil
	}
	name := p.stack.Metadata.Name
	if so.provider == nil {
		sec, err := e.store.Load(filepath.Join(so.stack.Dir, fleet.SecretsFile))
		if err != nil {
			return fmt.Errorf("%s: sign-on: provider %s: %w", name, so.stack.Metadata.Name, err)
		}
		if so.provider, err = providers[so.stack.Spec.Template.Name](so.issuer, sec); err != nil {
			return fmt.Errorf("%s: sign-on: provider %s: %w", name, so.stack.Metadata.Name, err)
		}
	}
	ch, err := so.provider.Plan(ctx, so.client)
	if err != nil {
		return fmt.Errorf("%s: sign-on: provider %s can't be asked, so the stack waits: %w", name, so.stack.Metadata.Name, err)
	}
	so.change = ch
	if ch.Empty() {
		return nil
	}
	kind, verb := deploy.Changed, "update"
	if ch.Create {
		kind, verb = deploy.Added, "create"
	}
	p.plan.Changes = append(p.plan.Changes, deploy.Change{Path: so.path(), Kind: kind, Diff: strings.Join(ch.Diff, "\n") + "\n"})
	// First: the client exists before the stack starts and signs in.
	step := deploy.Step{Kind: "sign-on", Text: fmt.Sprintf("%s the OpenID Connect client %s in %s (%s)", verb, so.client.ID, so.stack.Metadata.Name, so.provider.Kind())}
	p.plan.Steps = append([]deploy.Step{step}, p.plan.Steps...)
	return nil
}

// ensureSignOn registers the stack's client as planned.
func ensureSignOn(ctx context.Context, p *prepared) error {
	so := p.signOn
	if so == nil || so.change.Empty() {
		return nil
	}
	if err := so.provider.Ensure(ctx, so.client); err != nil {
		return fmt.Errorf("sign-on: %w", err)
	}
	return nil
}

// onlySignOn: the plan changes nothing but the client in the provider, so
// the stack itself needn't be touched.
func onlySignOn(p *prepared) bool {
	if p.signOn == nil || p.plan.Migration != nil || p.plan.MigrationError != "" {
		return false
	}
	path := p.signOn.path()
	for _, c := range p.plan.Changes {
		if c.Path != path {
			return false
		}
	}
	return len(p.plan.Changes) > 0
}
