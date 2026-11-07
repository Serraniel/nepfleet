// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Serraniel/nepfleet/internal/auth"
)

const (
	oidcStateCookie = "nepfleet_oidc_"
	oidcCookiePath  = "/api/v1/auth/oidc/"
	oidcCallback    = "/api/v1/auth/oidc/callback"
	oidcTimeout     = 15 * time.Second
	// A sign-in may take a while at the provider.
	oidcLifetime = 10 * time.Minute
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
	key      []byte
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

// oidcPending is a sign-in underway. It lives in the browser, not here, so nobody
// else's requests can push it out, and tabs don't get in each other's way.
type oidcPending struct {
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Return   string `json:"r"`
	Expires  int64  `json:"e"`
}

func (o *OIDC) sign(value string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.key == nil {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return "", err
		}
		o.key = key
	}
	mac := hmac.New(sha256.New, o.key)
	mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// The state is the cookie's signature, so an answer fits only the browser that
// asked, and only a cookie this server made. Its name is part of that state.
func oidcCookieName(state string) string {
	return oidcStateCookie + state[:16]
}

func oidcCookie(name, value string, maxAge int) *http.Cookie {
	// Lax: the browser comes back here from another site.
	return &http.Cookie{Name: name, Value: value, Path: oidcCookiePath, MaxAge: maxAge, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
}

func (o *OIDC) readPending(r *http.Request, now time.Time) (*oidcPending, string, error) {
	state := r.URL.Query().Get("state")
	if len(state) < 16 {
		return nil, "", errors.New("the answer has no state")
	}
	name := oidcCookieName(state)
	cookie, err := r.Cookie(name)
	if err != nil {
		return nil, "", errors.New("the answer isn't for this browser")
	}
	want, err := o.sign(cookie.Value)
	if err != nil {
		return nil, "", err
	}
	if !hmac.Equal([]byte(want), []byte(state)) {
		return nil, "", errors.New("the answer isn't for this browser")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return nil, "", err
	}
	var p oidcPending
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, "", err
	}
	if now.Unix() > p.Expires {
		return nil, "", errors.New("the sign-in took too long")
	}
	return &p, name, nil
}

type oidcClaims struct {
	Subject           string
	Email             string
	EmailVerified     bool
	PreferredUsername string
	Name              string
	Groups            []string
}

// parseClaims reads what providers send in their own ways. One oddly typed claim
// mustn't refuse someone a sub: entry would let in.
func parseClaims(m map[string]any) oidcClaims {
	text := func(key string) string {
		s, _ := m[key].(string)
		return s
	}
	c := oidcClaims{Subject: text("sub"), Email: text("email"), PreferredUsername: text("preferred_username"), Name: text("name")}
	switch v := m["email_verified"].(type) {
	case bool:
		c.EmailVerified = v
	case string:
		c.EmailVerified = v == "true"
	}
	switch v := m["groups"].(type) {
	case string:
		c.Groups = []string{v}
	case []any:
		for _, g := range v {
			if s, ok := g.(string); ok {
				c.Groups = append(c.Groups, s)
			}
		}
	}
	return c
}

// sameEmail ignores the case of ASCII only: Unicode folding makes other addresses
// match, such as a K from the Kelvin sign.
func sameEmail(a, b string) bool {
	lower := func(s string) string {
		out := []byte(s)
		for i, c := range out {
			if c >= 'A' && c <= 'Z' {
				out[i] = c + 'a' - 'A'
			}
		}
		return string(out)
	}
	return lower(a) == lower(b)
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
			if c.EmailVerified && c.Email != "" && sameEmail(a, c.Email) {
				return true
			}
		}
	}
	return false
}

// withUserInfo asks the provider for what its ID tokens leave out (Authelia does,
// for email and groups). It's the same person only if the subject is the same.
func withUserInfo(ctx context.Context, p *oidc.Provider, token *oauth2.Token, c oidcClaims) oidcClaims {
	info, err := p.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil || info.Subject != c.Subject {
		return c
	}
	var m map[string]any
	if err := info.Claims(&m); err != nil {
		return c
	}
	extra := parseClaims(m)
	if c.Email == "" {
		c.Email, c.EmailVerified = extra.Email, extra.EmailVerified
	}
	if len(c.Groups) == 0 {
		c.Groups = extra.Groups
	}
	if c.PreferredUsername == "" {
		c.PreferredUsername = extra.PreferredUsername
	}
	return c
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
		if auth.ValidUserName(n) && utf8.ValidString(n) {
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

// oidcRoutes take a route that always tries: the state can't be guessed, and noise
// from a shared address mustn't block everyone's sign-in.
func (a *Accounts) oidcRoutes(mux *http.ServeMux, open guardedRoute, origins []string) {
	o := a.OIDC
	mux.HandleFunc("GET /api/v1/auth/oidc/login", open(func(w http.ResponseWriter, r *http.Request, _ string) error {
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
		raw, err := json.Marshal(oidcPending{Nonce: nonce, Verifier: verifier, Return: returnURL(r.URL.Query().Get("return"), origins), Expires: a.now().Add(oidcLifetime).Unix()})
		if err != nil {
			return err
		}
		value := base64.RawURLEncoding.EncodeToString(raw)
		state, err := o.sign(value)
		if err != nil {
			return err
		}
		http.SetCookie(w, oidcCookie(oidcCookieName(state), value, int(oidcLifetime.Seconds())))
		http.Redirect(w, r, o.config(p).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
		return nil
	}))
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", open(func(w http.ResponseWriter, r *http.Request, _ string) error {
		q := r.URL.Query()
		pending, name, err := o.readPending(r, a.now())
		if err != nil {
			return err
		}
		http.SetCookie(w, oidcCookie(name, "", -1))
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
		// The provider takes a code once, which stops a replay.
		token, err := o.config(p).Exchange(ctx, q.Get("code"), oauth2.VerifierOption(pending.Verifier))
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
		if !hmac.Equal([]byte(id.Nonce), []byte(pending.Nonce)) {
			return errors.New("ID token: wrong nonce")
		}
		if id.Subject == "" {
			return errors.New("ID token: no subject")
		}
		var m map[string]any
		if err := id.Claims(&m); err != nil {
			return fmt.Errorf("ID token: %w", err)
		}
		claims := parseClaims(m)
		claims.Subject = id.Subject
		if !o.allowed(claims) {
			claims = withUserInfo(ctx, p, token, claims)
		}
		if !o.allowed(claims) {
			return fmt.Errorf("%q isn't on --oidc-allow", claims.Subject)
		}
		u, err := a.Users.EnsureExternal(oidcUserID(o.Issuer, id.Subject), claims.displayName())
		if err != nil {
			return err
		}
		// A user exists now, even if they're removed later: the logged token is spent.
		a.mu.Lock()
		a.SetupToken = ""
		a.mu.Unlock()
		log.Printf("api: %q signed in through the identity provider", u.Name) //nolint:gosec // a checked name, and %q escapes it
		if err := a.signIn(w, u); err != nil {
			return err
		}
		// Again here: a cookie can be set by a sibling domain.
		http.Redirect(w, r, returnURL(pending.Return, origins), http.StatusSeeOther)
		return nil
	}))
}
