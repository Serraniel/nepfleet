// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/Serraniel/nepfleet/internal/auth"
)

const oidcUI = "https://fleet.example.test"

// fakeProvider is an identity provider: discovery, keys, and tokens it signs.
type fakeProvider struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	signer jose.Signer
	// claims go into the next ID token; change adds or breaks things.
	claims   map[string]any
	nonce    string
	verifier string
	change   func(map[string]any)
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{t: t, key: key, signer: signer, claims: map[string]any{
		"sub": "person-1", "email": "ada@example.test", "email_verified": true, "preferred_username": "ada",
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/authorize", "token_endpoint": p.srv.URL + "/token",
			"jwks_uri": p.srv.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: key.Public(), KeyID: "k", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		p.verifier = r.Form.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "token_type": "Bearer", "id_token": p.token()})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeProvider) token() string {
	p.t.Helper()
	c := map[string]any{"iss": p.srv.URL, "aud": "client", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": p.nonce}
	for k, v := range p.claims {
		c[k] = v
	}
	if p.change != nil {
		p.change(c)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		p.t.Fatal(err)
	}
	s, err := p.signer.Sign(raw)
	if err != nil {
		p.t.Fatal(err)
	}
	out, err := s.CompactSerialize()
	if err != nil {
		p.t.Fatal(err)
	}
	return out
}

type oidcHarness struct {
	t *testing.T
	p *fakeProvider
	h http.Handler
	a *Accounts
}

func newOIDCHarness(t *testing.T, allow ...string) *oidcHarness {
	t.Helper()
	p := newFakeProvider(t)
	o := &OIDC{Issuer: p.srv.URL, ClientID: "client", ClientSecret: "secret", RedirectURL: "https://api.example.test" + oidcCallback, Allow: allow}
	a := &Accounts{Users: &auth.UserStore{Dir: t.TempDir()}, Sessions: &auth.Sessions{}, OIDC: o}
	h := Handler(Options{Accounts: a, AllowedOrigins: []string{oidcUI}, Tokens: &auth.Store{Dir: t.TempDir()},
		Stacks: func() ([]StackInfo, error) { return nil, nil }})
	return &oidcHarness{t: t, p: p, h: h, a: a}
}

var oidcAddr int

func (h *oidcHarness) get(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	oidcAddr++
	req.RemoteAddr = "10.1.0." + string(rune('0'+oidcAddr%10)) + ":1"
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

// begin starts a sign-in and returns the provider's URL and the state cookie.
func (h *oidcHarness) begin(ret string) (*url.URL, *http.Cookie) {
	h.t.Helper()
	rec := h.get("/api/v1/auth/oidc/login?return=" + url.QueryEscape(ret))
	if rec.Code != http.StatusFound {
		h.t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		h.t.Fatal(err)
	}
	var state *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcStateCookie {
			state = c
		}
	}
	if state == nil || !state.HttpOnly || !state.Secure || state.SameSite != http.SameSiteLaxMode {
		h.t.Fatalf("state cookie: %+v", state)
	}
	h.p.nonce = loc.Query().Get("nonce")
	return loc, state
}

func (h *oidcHarness) finish(loc *url.URL, state *http.Cookie) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.get(oidcCallback+"?code=c&state="+url.QueryEscape(loc.Query().Get("state")), state)
}

func TestOIDCSignIn(t *testing.T) {
	h := newOIDCHarness(t, "ada@example.test")
	loc, state := h.begin(oidcUI + "/stacks")
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("client_id") != "client" {
		t.Errorf("authorize: %s", loc)
	}
	rec := h.finish(loc, state)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != oidcUI+"/stacks" {
		t.Fatalf("callback: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	if h.p.verifier == "" {
		t.Error("PKCE verifier wasn't sent")
	}
	session := sessionCookie(t, rec)
	if !session.HttpOnly || !session.Secure {
		t.Errorf("%+v", session)
	}
	// The session works, as that person.
	got := h.get("/api/v1/auth/status", session)
	if !strings.Contains(got.Body.String(), `"name":"ada"`) || !strings.Contains(got.Body.String(), `"signedIn":true`) {
		t.Errorf("status: %s", got.Body)
	}
	if got := h.get("/api/v1/stacks", session); got.Code != http.StatusOK {
		t.Errorf("stacks: %d", got.Code)
	}
	// The second sign-in is the same user.
	loc, state = h.begin(oidcUI)
	if rec := h.finish(loc, state); rec.Code != http.StatusSeeOther {
		t.Fatalf("again: %d", rec.Code)
	}
	if users, _ := h.a.Users.List(); len(users) != 1 {
		t.Errorf("users: %+v", users)
	}
	// Removing them ends it.
	if err := h.a.Users.Remove("ada"); err != nil {
		t.Fatal(err)
	}
	if got := h.get("/api/v1/stacks", session); got.Code != http.StatusUnauthorized {
		t.Errorf("removed: %d", got.Code)
	}
}

func TestOIDCRefuses(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"wrong nonce":      func(c map[string]any) { c["nonce"] = "other" },
		"wrong audience":   func(c map[string]any) { c["aud"] = "someone-else" },
		"wrong issuer":     func(c map[string]any) { c["iss"] = "https://evil.example.test" },
		"expired":          func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"unverified email": func(c map[string]any) { c["email_verified"] = false },
		"someone else":     func(c map[string]any) { c["email"] = "eve@example.test" },
		"no email":         func(c map[string]any) { delete(c, "email") },
	} {
		t.Run(name, func(t *testing.T) {
			h := newOIDCHarness(t, "ada@example.test")
			h.p.change = change
			loc, state := h.begin(oidcUI)
			rec := h.finish(loc, state)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%d %s", rec.Code, rec.Body)
			}
			for _, c := range rec.Result().Cookies() {
				if c.Name == SessionCookie {
					t.Error("got a session")
				}
			}
			if users, _ := h.a.Users.List(); len(users) != 0 {
				t.Errorf("a user was created: %+v", users)
			}
		})
	}
}

func TestOIDCBadSignature(t *testing.T) {
	h := newOIDCHarness(t, "ada@example.test")
	other := newFakeProvider(t)
	h.p.signer = other.signer
	loc, state := h.begin(oidcUI)
	if rec := h.finish(loc, state); rec.Code != http.StatusUnauthorized {
		t.Errorf("%d", rec.Code)
	}
}

func TestOIDCStateIsTiedToTheBrowser(t *testing.T) {
	h := newOIDCHarness(t, "ada@example.test")
	loc, state := h.begin(oidcUI)
	// Someone else's answer, in a browser that didn't start this.
	if rec := h.get(oidcCallback + "?code=c&state=" + url.QueryEscape(loc.Query().Get("state"))); rec.Code != http.StatusUnauthorized {
		t.Errorf("no cookie: %d", rec.Code)
	}
	if rec := h.get(oidcCallback+"?code=c&state=other", state); rec.Code != http.StatusUnauthorized {
		t.Errorf("other state: %d", rec.Code)
	}
	// It worked once: a replay doesn't.
	if rec := h.finish(loc, state); rec.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := h.finish(loc, state); rec.Code != http.StatusUnauthorized {
		t.Errorf("replay: %d", rec.Code)
	}
}

func TestOIDCAllowList(t *testing.T) {
	o := &OIDC{Allow: []string{"ada@example.test", "sub:abc", "group:admins"}}
	for _, tc := range []struct {
		c    oidcClaims
		want bool
	}{
		{oidcClaims{Email: "ADA@example.test", EmailVerified: true}, true},
		{oidcClaims{Email: "ada@example.test"}, false},
		{oidcClaims{Subject: "abc"}, true},
		{oidcClaims{Subject: "sub:abc"}, false},
		{oidcClaims{Groups: []string{"users", "admins"}}, true},
		{oidcClaims{Groups: []string{"group:admins"}}, false},
		{oidcClaims{Email: "group:admins", EmailVerified: true}, false},
		{oidcClaims{}, false},
	} {
		if got := o.allowed(tc.c); got != tc.want {
			t.Errorf("%+v: %v", tc.c, got)
		}
	}
}

func TestOIDCPeopleAreKeptApart(t *testing.T) {
	h := newOIDCHarness(t, "group:admins")
	h.p.claims = map[string]any{"sub": "one", "preferred_username": "ada", "groups": []string{"admins"}}
	loc, state := h.begin(oidcUI)
	if rec := h.finish(loc, state); rec.Code != http.StatusSeeOther {
		t.Fatal(rec.Code, rec.Body)
	}
	// Another person with the same username is another user, not the first.
	h.p.claims["sub"] = "two"
	loc, state = h.begin(oidcUI)
	rec := h.finish(loc, state)
	if rec.Code != http.StatusSeeOther {
		t.Fatal(rec.Code, rec.Body)
	}
	got := h.get("/api/v1/auth/status", sessionCookie(t, rec))
	if strings.Contains(got.Body.String(), `"name":"ada"`) {
		t.Errorf("signed in under the first person's name: %s", got.Body)
	}
	if users, _ := h.a.Users.List(); len(users) != 2 {
		t.Errorf("%+v", users)
	}
}

func TestReturnURL(t *testing.T) {
	origins := []string{oidcUI}
	for in, want := range map[string]string{
		oidcUI + "/stacks":             oidcUI + "/stacks",
		"https://evil.example.test/":   oidcUI,
		oidcUI + ".evil.example.test/": oidcUI,
		"//evil.example.test/":         oidcUI,
		"javascript:alert(1)":          oidcUI,
		"http://fleet.example.test/x":  oidcUI,
		"":                             oidcUI,
	} {
		if got := returnURL(in, origins); got != want {
			t.Errorf("%q: %s", in, got)
		}
	}
	if got := returnURL("https://evil.example.test", nil); got != "/" {
		t.Errorf("no origins: %s", got)
	}
}

func TestCheckOIDC(t *testing.T) {
	good := OIDC{Issuer: "https://id.example.test", ClientID: "c", ClientSecret: "s", RedirectURL: "https://api.example.test" + oidcCallback, Allow: []string{"a@b"}}
	if err := good.CheckOIDC(); err != nil {
		t.Error(err)
	}
	for name, change := range map[string]func(*OIDC){
		"plain issuer":   func(o *OIDC) { o.Issuer = "http://id.example.test" },
		"no issuer":      func(o *OIDC) { o.Issuer = "" },
		"plain redirect": func(o *OIDC) { o.RedirectURL = "http://api.example.test" + oidcCallback },
		"other path":     func(o *OIDC) { o.RedirectURL = "https://api.example.test/x" },
		"no client":      func(o *OIDC) { o.ClientID = "" },
		"no secret":      func(o *OIDC) { o.ClientSecret = "" },
		"nobody allowed": func(o *OIDC) { o.Allow = nil },
	} {
		o := OIDC{Issuer: good.Issuer, ClientID: good.ClientID, ClientSecret: good.ClientSecret, RedirectURL: good.RedirectURL, Allow: good.Allow}
		change(&o)
		if o.CheckOIDC() == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

// Without passkeys, their endpoints don't exist, and don't crash.
func TestOIDCWithoutPasskeys(t *testing.T) {
	h := newOIDCHarness(t, "a@b")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/begin", nil)
	req.Header.Set("Origin", oidcUI)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	if rec.Code < 400 || rec.Code >= 500 {
		t.Errorf("%d", rec.Code)
	}
}
