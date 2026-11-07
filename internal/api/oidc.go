// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Serraniel/nepfleet/internal/auth"
)

const (
	oidcStateCookie = "nepfleet_oidc_state"
	oidcCookiePath  = "/api/v1/auth/oidc/"
	oidcCallback    = "/api/v1/auth/oidc/callback"
	oidcTimeout     = 15 * time.Second
)

// OIDC is signing in through an identity provider (Pocket ID, Authelia, …). A
// provider often lets many people have accounts, so Allow says who may come in;
// a person who signs in can do everything.
type OIDC struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL is this server's callback, as the provider knows it.
	RedirectURL string
	// Allow holds verified email addresses, sub:<subject> and group:<name>.
	Allow []string

	mu       sync.Mutex
	provider *oidc.Provider
}

// CheckOIDC checks settings before anything listens: nobody allowed would leave
// sign-in pointless, and a plain-HTTP issuer could say anything about anyone.
func (o *OIDC) CheckOIDC() error {
	u, err := url.Parse(o.Issuer)
	if err != nil || u.Host == "" {
		return fmt.Errorf("issuer %q is not a URL", o.Issuer)
	}
	if u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" {
		return fmt.Errorf("issuer %q must use https", o.Issuer)
	}
	cb, err := url.Parse(o.RedirectURL)
	if err != nil || cb.Host == "" || cb.Path != oidcCallback {
		return fmt.Errorf("redirect URL %q must end in %s", o.RedirectURL, oidcCallback)
	}
	if cb.Scheme != "https" && cb.Hostname() != "localhost" && cb.Hostname() != "127.0.0.1" {
		return fmt.Errorf("redirect URL %q must use https", o.RedirectURL)
	}
	switch {
	case o.ClientID == "":
		return errors.New("needs a client ID")
	case o.ClientSecret == "":
		return errors.New("needs a client secret")
	case len(o.Allow) == 0:
		return errors.New("needs a list of who may sign in")
	}
	return nil
}

// discover asks the provider about itself, when first needed: a provider that is
// down mustn't keep the server from starting, nor stay unusable after it's back.
func (o *OIDC) discover(ctx context.Context) (*oidc.Provider, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider != nil {
		return o.provider, nil
	}
	// Not the request's context: the provider keeps it to fetch signing keys.
	p, err := oidc.NewProvider(oidc.ClientContext(context.WithoutCancel(ctx), &http.Client{Timeout: oidcTimeout}), o.Issuer)
	if err != nil {
		return nil, err
	}
	o.provider = p
	return p, nil
}

func (o *OIDC) config(p *oidc.Provider) *oauth2.Config {
	scopes := []string{oidc.ScopeOpenID, "profile", "email"}
	// Only when needed: some providers refuse a scope they don't know.
	if slices.ContainsFunc(o.Allow, func(a string) bool { return strings.HasPrefix(a, "group:") }) {
		scopes = append(scopes, "groups")
	}
	return &oauth2.Config{
		ClientID: o.ClientID, ClientSecret: o.ClientSecret, RedirectURL: o.RedirectURL,
		Endpoint: p.Endpoint(), Scopes: scopes,
	}
}

type oidcClaims struct {
	Subject           string   `json:"sub"`
	Email             string   `json:"email"`
	EmailVerified     bool     `json:"email_verified"`
	PreferredUsername string   `json:"preferred_username"`
	Name              string   `json:"name"`
	Groups            []string `json:"groups"`
}

// allowed says whether the list lets someone in. An email counts only if the
// provider has verified it, or anyone could claim one.
func (o *OIDC) allowed(c oidcClaims) bool {
	for _, a := range o.Allow {
		switch {
		case strings.HasPrefix(a, "sub:"):
			if c.Subject != "" && strings.TrimPrefix(a, "sub:") == c.Subject {
				return true
			}
		case strings.HasPrefix(a, "group:"):
			if slices.Contains(c.Groups, strings.TrimPrefix(a, "group:")) {
				return true
			}
		default:
			if c.EmailVerified && c.Email != "" && strings.EqualFold(a, c.Email) {
				return true
			}
		}
	}
	return false
}

// oidcUserID is who someone is: their provider's subject, never a name or email,
// which can change hands.
func oidcUserID(issuer, subject string) []byte {
	sum := sha256.Sum256([]byte(issuer + "\x00" + subject))
	return sum[:]
}

func (c oidcClaims) displayName() string {
	email, _, _ := strings.Cut(c.Email, "@")
	for _, n := range []string{c.PreferredUsername, c.Name, email} {
		if auth.ValidUserName(n) {
			return n
		}
	}
	return "oidc-user"
}

// returnURL keeps where to go afterwards only if it's in an allowed origin: this
// must not send people to other sites.
func returnURL(raw string, origins []string) string {
	u, err := url.Parse(raw)
	if err == nil && u.Scheme != "" && u.Host != "" && slices.Contains(origins, u.Scheme+"://"+u.Host) {
		return raw
	}
	if len(origins) > 0 {
		return origins[0]
	}
	return "/"
}

func (a *Accounts) oidcRoutes(mux *http.ServeMux, guarded guardedRoute, origins []string) {
	o := a.OIDC
	mux.HandleFunc("GET /api/v1/auth/oidc/login", guarded(func(w http.ResponseWriter, r *http.Request, addr string) error {
		ctx, cancel := context.WithTimeout(r.Context(), oidcTimeout)
		defer cancel()
		p, err := o.discover(ctx)
		if err != nil {
			return fmt.Errorf("identity provider: %w", err)
		}
		nonce, err := randomToken()
		if err != nil {
			return err
		}
		verifier := oauth2.GenerateVerifier()
		state, err := a.store(ceremony{kind: kindOIDC, addr: addr, nonce: nonce, verifier: verifier, returnTo: returnURL(r.URL.Query().Get("return"), origins)})
		if err != nil {
			return err
		}
		// Ties the answer to this browser. Lax: it comes back from another site.
		http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Value: state, Path: oidcCookiePath, MaxAge: int(ceremonyLifetime.Seconds()),
			HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		http.Redirect(w, r, o.config(p).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
		return nil
	}))
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", guarded(func(w http.ResponseWriter, r *http.Request, _ string) error {
		q := r.URL.Query()
		state := q.Get("state")
		cookie, err := r.Cookie(oidcStateCookie)
		if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
			return errors.New("the answer isn't for this browser")
		}
		c, ok := a.take(state, kindOIDC)
		if !ok {
			return errors.New("unknown or old sign-in")
		}
		http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Path: oidcCookiePath, MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
		if q.Get("error") != "" {
			return fmt.Errorf("the provider refused: %.100q", q.Get("error"))
		}
		ctx, cancel := context.WithTimeout(r.Context(), oidcTimeout)
		defer cancel()
		ctx = oidc.ClientContext(ctx, &http.Client{Timeout: oidcTimeout})
		p, err := o.discover(ctx)
		if err != nil {
			return fmt.Errorf("identity provider: %w", err)
		}
		token, err := o.config(p).Exchange(ctx, q.Get("code"), oauth2.VerifierOption(c.verifier))
		if err != nil {
			return fmt.Errorf("exchanging the code: %w", err)
		}
		raw, ok := token.Extra("id_token").(string)
		if !ok {
			return errors.New("no ID token in the answer")
		}
		id, err := p.Verifier(&oidc.Config{ClientID: o.ClientID}).Verify(ctx, raw)
		if err != nil {
			return fmt.Errorf("ID token: %w", err)
		}
		if subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(c.nonce)) != 1 {
			return errors.New("ID token: wrong nonce")
		}
		var claims oidcClaims
		if err := id.Claims(&claims); err != nil {
			return fmt.Errorf("ID token: %w", err)
		}
		// Already checked by the library against the signed token.
		claims.Subject = id.Subject
		if !o.allowed(claims) {
			return fmt.Errorf("%q isn't on --oidc-allow", claims.Subject)
		}
		u, err := a.Users.EnsureExternal(oidcUserID(o.Issuer, id.Subject), claims.displayName())
		if err != nil {
			return err
		}
		log.Printf("api: %q signed in through the identity provider", u.Name)
		if err := a.signIn(w, u); err != nil {
			return err
		}
		http.Redirect(w, r, c.returnTo, http.StatusSeeOther)
		return nil
	}))
}
