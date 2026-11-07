// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"

	"github.com/Serraniel/nepfleet/internal/auth"
)

const (
	testRP     = "fleet.example.test"
	testOrigin = "https://fleet.example.test"
	setupToken = "the-setup-token"
)

type loginHarness struct {
	t     *testing.T
	h     http.Handler
	a     *Accounts
	rp    virtualwebauthn.RelyingParty
	key   virtualwebauthn.Authenticator
	cred  virtualwebauthn.Credential
	calls int
}

func newLoginHarness(t *testing.T) *loginHarness {
	t.Helper()
	w, err := NewWebAuthn(testRP, []string{testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	a := &Accounts{Users: &auth.UserStore{Dir: dir}, Sessions: &auth.Sessions{}, WebAuthn: w, SetupToken: setupToken}
	h := Handler(Options{Accounts: a, AllowedOrigins: []string{testOrigin}, Tokens: &auth.Store{Dir: t.TempDir()},
		Stacks: func() ([]StackInfo, error) { return []StackInfo{{Name: "web"}}, nil }})
	return &loginHarness{t: t, h: h, a: a,
		rp:   virtualwebauthn.RelyingParty{Name: "NepFleet", ID: testRP, Origin: testOrigin},
		key:  virtualwebauthn.NewAuthenticator(),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)}
}

// post sends a request as the web UI would, from its origin (addr varies, so a
// test's many deliberate failures don't lock it out).
func (l *loginHarness) do(method, path, body, cookie, origin string) *httptest.ResponseRecorder {
	l.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "api.example.test"
	l.calls++
	req.RemoteAddr = "10.0.0." + string(rune('0'+l.calls%10)) + ":1"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	}
	rec := httptest.NewRecorder()
	l.h.ServeHTTP(rec, req)
	return rec
}

func (l *loginHarness) begin(path, body string) (string, string) {
	l.t.Helper()
	rec := l.do(http.MethodPost, path, body, "", testOrigin)
	if rec.Code != http.StatusOK {
		l.t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
	}
	var out struct {
		Ceremony string          `json:"ceremony"`
		Options  json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		l.t.Fatal(err)
	}
	return out.Ceremony, string(out.Options)
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	t.Fatalf("no session cookie: %d %s", rec.Code, rec.Body)
	return nil
}

func (l *loginHarness) setup() string {
	l.t.Helper()
	id, options := l.begin("/api/v1/auth/setup/begin", `{"setupToken":"`+setupToken+`","name":"Ada"}`)
	opts, err := virtualwebauthn.ParseAttestationOptions(options)
	if err != nil {
		l.t.Fatal(err)
	}
	resp := virtualwebauthn.CreateAttestationResponse(l.rp, l.key, l.cred, *opts)
	rec := l.do(http.MethodPost, "/api/v1/auth/setup/finish?ceremony="+id, resp, "", testOrigin)
	if rec.Code != http.StatusNoContent {
		l.t.Fatalf("finish: %d %s", rec.Code, rec.Body)
	}
	// The authenticator now also knows who the passkey belongs to.
	users, err := l.a.Users.List()
	if err != nil || len(users) != 1 {
		l.t.Fatalf("users: %v %v", users, err)
	}
	l.key.Options.UserHandle = users[0].ID
	l.key.AddCredential(l.cred)
	return sessionCookie(l.t, rec).Value
}

func (l *loginHarness) assertion() (string, string) {
	l.t.Helper()
	id, options := l.begin("/api/v1/auth/login/begin", ``)
	opts, err := virtualwebauthn.ParseAssertionOptions(options)
	if err != nil {
		l.t.Fatal(err)
	}
	return id, virtualwebauthn.CreateAssertionResponse(l.rp, l.key, l.cred, *opts)
}

func TestPasskeyFlow(t *testing.T) {
	l := newLoginHarness(t)

	rec := l.do(http.MethodGet, "/api/v1/auth/status", "", "", "")
	if !strings.Contains(rec.Body.String(), `"setupNeeded":true`) {
		t.Errorf("status: %s", rec.Body)
	}
	if rec := l.do(http.MethodPost, "/api/v1/auth/setup/begin", `{"setupToken":"wrong","name":"Eve"}`, "", testOrigin); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong setup token: %d", rec.Code)
	}
	if rec := l.do(http.MethodPost, "/api/v1/auth/setup/begin", `{"setupToken":"`+setupToken+`","name":"Ada"}`, "", "https://evil.example"); rec.Code != http.StatusForbidden {
		t.Errorf("from another site: %d", rec.Code)
	}
	if rec := l.do(http.MethodGet, "/api/v1/stacks", "", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("before signing in: %d", rec.Code)
	}

	session := l.setup()
	if rec := l.do(http.MethodGet, "/api/v1/stacks", "", session, ""); rec.Code != http.StatusOK {
		t.Errorf("signed in by setup: %d", rec.Code)
	}

	// The token is spent.
	if rec := l.do(http.MethodPost, "/api/v1/auth/setup/begin", `{"setupToken":"`+setupToken+`","name":"Eve"}`, "", testOrigin); rec.Code != http.StatusUnauthorized {
		t.Errorf("setup token used twice: %d", rec.Code)
	}
	if rec := l.do(http.MethodGet, "/api/v1/auth/status", "", session, ""); !strings.Contains(rec.Body.String(), `"setupNeeded":false`) || !strings.Contains(rec.Body.String(), `"name":"Ada"`) {
		t.Errorf("status: %s", rec.Body)
	}

	// Signing in with the passkey.
	id, resp := l.assertion()
	rec = l.do(http.MethodPost, "/api/v1/auth/login/finish?ceremony="+id, resp, "", testOrigin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	c := sessionCookie(t, rec)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/api/" {
		t.Errorf("cookie: %+v", c)
	}
	if rec := l.do(http.MethodGet, "/api/v1/stacks", "", c.Value, ""); rec.Code != http.StatusOK {
		t.Errorf("after login: %d", rec.Code)
	}

	// A challenge can't be answered twice.
	if rec := l.do(http.MethodPost, "/api/v1/auth/login/finish?ceremony="+id, resp, "", testOrigin); rec.Code != http.StatusUnauthorized {
		t.Errorf("replay: %d", rec.Code)
	}
	// Signing out ends it.
	if rec := l.do(http.MethodPost, "/api/v1/auth/logout", "", c.Value, testOrigin); rec.Code != http.StatusNoContent {
		t.Errorf("logout: %d", rec.Code)
	}
	if rec := l.do(http.MethodGet, "/api/v1/stacks", "", c.Value, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("after logout: %d", rec.Code)
	}
}

func TestPasskeyAttacks(t *testing.T) {
	l := newLoginHarness(t)
	session := l.setup()

	// A passkey made for a look-alike site doesn't work here.
	id, options := l.begin("/api/v1/auth/login/begin", ``)
	opts, err := virtualwebauthn.ParseAssertionOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	phish := virtualwebauthn.RelyingParty{Name: "x", ID: testRP, Origin: "https://fleet.example.test.evil.example"}
	resp := virtualwebauthn.CreateAssertionResponse(phish, l.key, l.cred, *opts)
	if rec := l.do(http.MethodPost, "/api/v1/auth/login/finish?ceremony="+id, resp, "", testOrigin); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong origin: %d", rec.Code)
	}

	// A key the server never saw, claiming to be the user.
	id, options = l.begin("/api/v1/auth/login/begin", ``)
	opts, err = virtualwebauthn.ParseAssertionOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	other := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	resp = virtualwebauthn.CreateAssertionResponse(l.rp, l.key, other, *opts)
	if rec := l.do(http.MethodPost, "/api/v1/auth/login/finish?ceremony="+id, resp, "", testOrigin); rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown key: %d", rec.Code)
	}

	// Another site's page can't use the cookie for a change, or without Origin.
	if rec := l.do(http.MethodPost, "/api/v1/stacks/web/apply", `{"plan":"x"}`, session, "https://evil.example"); rec.Code != http.StatusForbidden {
		t.Errorf("CSRF: %d", rec.Code)
	}
	if rec := l.do(http.MethodPost, "/api/v1/stacks/web/apply", `{"plan":"x"}`, session, ""); rec.Code != http.StatusForbidden {
		t.Errorf("no Origin: %d", rec.Code)
	}

	// A made-up session is nothing.
	if rec := l.do(http.MethodGet, "/api/v1/stacks", "", "made-up", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("made-up session: %d", rec.Code)
	}

	// A second passkey needs a session; a login ceremony can't finish it.
	if rec := l.do(http.MethodPost, "/api/v1/auth/passkeys/begin", "", "", testOrigin); rec.Code != http.StatusUnauthorized {
		t.Errorf("second passkey without a session: %d", rec.Code)
	}
	login, _ := l.begin("/api/v1/auth/login/begin", ``)
	if rec := l.do(http.MethodPost, "/api/v1/auth/passkeys/finish?ceremony="+login, "{}", session, testOrigin); rec.Code != http.StatusUnauthorized {
		t.Errorf("a login ceremony used to add a key: %d", rec.Code)
	}
}

func TestSecondPasskey(t *testing.T) {
	l := newLoginHarness(t)
	session := l.setup()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/passkeys/begin", nil)
	req.Header.Set("Origin", testOrigin)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: session})
	rec := httptest.NewRecorder()
	l.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		Ceremony string          `json:"ceremony"`
		Options  json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	opts, err := virtualwebauthn.ParseAttestationOptions(string(out.Options))
	if err != nil {
		t.Fatal(err)
	}
	second := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	resp := virtualwebauthn.CreateAttestationResponse(l.rp, l.key, second, *opts)
	if rec := l.do(http.MethodPost, "/api/v1/auth/passkeys/finish?ceremony="+out.Ceremony, resp, session, testOrigin); rec.Code != http.StatusNoContent {
		t.Fatalf("finish: %d %s", rec.Code, rec.Body)
	}
	users, _ := l.a.Users.List()
	if len(users) != 1 || len(users[0].Credentials) != 2 {
		t.Errorf("users: %+v", users)
	}
}

func TestSessionsExpire(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	s := &auth.Sessions{Now: func() time.Time { return now }}
	id, err := s.Create(&auth.User{ID: []byte("x"), Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	if _, name, ok := s.Check(id); !ok || name != "Ada" {
		t.Errorf("fresh: %v %v", name, ok)
	}
	now = now.Add(auth.SessionLifetime + time.Second)
	if _, _, ok := s.Check(id); ok {
		t.Error("expired session must fail")
	}
}

// A passkey ceremony held from before must not finish setup after a user is gone.
func TestHeldCeremonyCannotReopenSetup(t *testing.T) {
	l := newLoginHarness(t)
	session := l.setup()
	held := l.do(http.MethodPost, "/api/v1/auth/passkeys/begin", "", session, testOrigin)
	var out struct {
		Ceremony string          `json:"ceremony"`
		Options  json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(held.Body.Bytes(), &out); err != nil {
		t.Fatal(held.Body, err)
	}
	opts, err := virtualwebauthn.ParseAttestationOptions(string(out.Options))
	if err != nil {
		t.Fatal(err)
	}
	resp := virtualwebauthn.CreateAttestationResponse(l.rp, l.key, virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2), *opts)
	if err := l.a.Users.Remove("Ada"); err != nil {
		t.Fatal(err)
	}
	if rec := l.do(http.MethodPost, "/api/v1/auth/setup/finish?ceremony="+out.Ceremony, resp, "", testOrigin); rec.Code != http.StatusUnauthorized {
		t.Errorf("a passkey ceremony finished setup: %d", rec.Code)
	}
	if users, _ := l.a.Users.List(); len(users) != 0 {
		t.Errorf("a removed user came back: %+v", users)
	}
	// And their session ended with the removal.
	if rec := l.do(http.MethodGet, "/api/v1/stacks", "", session, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("a removed user's session: %d", rec.Code)
	}
}

// A setup ceremony from before the first user can't make a second one.
func TestHeldSetupCeremony(t *testing.T) {
	l := newLoginHarness(t)
	id, options := l.begin("/api/v1/auth/setup/begin", `{"setupToken":"`+setupToken+`","name":"Eve"}`)
	l.setup()
	opts, err := virtualwebauthn.ParseAttestationOptions(options)
	if err != nil {
		t.Fatal(err)
	}
	resp := virtualwebauthn.CreateAttestationResponse(l.rp, l.key, virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2), *opts)
	if rec := l.do(http.MethodPost, "/api/v1/auth/setup/finish?ceremony="+id, resp, "", testOrigin); rec.Code != http.StatusUnauthorized {
		t.Errorf("a stale setup ceremony: %d", rec.Code)
	}
	if users, _ := l.a.Users.List(); len(users) != 1 {
		t.Errorf("users: %+v", users)
	}
}

func TestUserVerificationRequired(t *testing.T) {
	l := newLoginHarness(t)
	_, options := l.begin("/api/v1/auth/setup/begin", `{"setupToken":"`+setupToken+`","name":"Ada"}`)
	if !strings.Contains(options, `"userVerification":"required"`) {
		t.Errorf("registration: %s", options)
	}
	l.setup()
	_, options = l.begin("/api/v1/auth/login/begin", ``)
	if !strings.Contains(options, `"userVerification":"required"`) {
		t.Errorf("login: %s", options)
	}
}

// from sends from one fixed address.
func (l *loginHarness) from(addr, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "api.example.test"
	req.RemoteAddr = addr + ":1"
	req.Header.Set("Origin", testOrigin)
	rec := httptest.NewRecorder()
	l.h.ServeHTTP(rec, req)
	return rec
}

func TestSignInSurvivesFloodAndNoise(t *testing.T) {
	l := newLoginHarness(t)
	l.setup()
	const attacker, victim = "198.51.100.7", "203.0.113.9"
	// Far more than the table holds, from one address and from many.
	for i := range maxCeremonies + 50 {
		if rec := l.from(attacker, http.MethodPost, "/api/v1/auth/login/begin", ""); rec.Code != http.StatusOK {
			t.Fatalf("begin %d: %d", i, rec.Code)
		}
	}
	l.a.mu.Lock()
	n := len(l.a.ceremonies)
	l.a.mu.Unlock()
	if n > maxCeremoniesPerAddr {
		t.Errorf("one address holds %d ceremonies", n)
	}
	// The victim signs in, even after noise from the same address.
	for range 12 {
		l.from(victim, http.MethodGet, "/api/v1/stacks", "")
	}
	rec := l.from(victim, http.MethodPost, "/api/v1/auth/login/begin", "")
	var out struct {
		Ceremony string          `json:"ceremony"`
		Options  json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(rec.Code, rec.Body, err)
	}
	opts, err := virtualwebauthn.ParseAssertionOptions(string(out.Options))
	if err != nil {
		t.Fatal(err)
	}
	resp := virtualwebauthn.CreateAssertionResponse(l.rp, l.key, l.cred, *opts)
	if rec := l.from(victim, http.MethodPost, "/api/v1/auth/login/finish?ceremony="+out.Ceremony, resp); rec.Code != http.StatusNoContent {
		t.Errorf("a real passkey from a noisy address: %d %s", rec.Code, rec.Body)
	}
	// Guessing the setup token stays locked out.
	for range 12 {
		l.from(attacker, http.MethodPost, "/api/v1/auth/setup/begin", `{"setupToken":"guess","name":"x"}`)
	}
	if rec := l.from(attacker, http.MethodPost, "/api/v1/auth/setup/begin", `{"setupToken":"guess","name":"x"}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("guessing the setup token: %d", rec.Code)
	}
}

func TestAddrKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.9":          "203.0.113.9",
		"2001:db8:1:2:aaaa::1": "2001:db8:1:2::",
		"2001:db8:1:2:bbbb::9": "2001:db8:1:2::",
		"not an address":       "not an address",
	} {
		if got := addrKey(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestSameOriginIsExact(t *testing.T) {
	allowed := []string{"https://fleet.example.test"}
	for origin, want := range map[string]bool{
		"https://fleet.example.test":        true,
		"http://fleet.example.test":         false, // another scheme
		"https://fleet.example.test:8443":   false,
		"https://api.example.test":          false, // the request's own host doesn't count
		"https://fleet.example.test.evil.x": false,
		"null":                              false,
		"":                                  false,
	} {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Host = "api.example.test"
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if got := sameOrigin(req, allowed); got != want {
			t.Errorf("%q: %v", origin, got)
		}
	}
}

func TestNames(t *testing.T) {
	for name, ok := range map[string]bool{"Ada": true, "हिन्दी": true, "Müller": true, "-x": false, "a<b": false, "a\nb": false, "": false} {
		if auth.ValidUserName(name) != ok {
			t.Errorf("%q", name)
		}
	}
}
