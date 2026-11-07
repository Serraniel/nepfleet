// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// SessionCookie holds a sign-in.
const SessionCookie = "nepfleet_session"

// Accounts is signing in with passkeys.
type Accounts struct {
	Users    *auth.UserStore
	Sessions *auth.Sessions
	WebAuthn *webauthn.WebAuthn
	// SetupToken lets the first user register; empty once users exist.
	SetupToken string

	mu         sync.Mutex
	ceremonies map[string]ceremony
}

// NewWebAuthn sets up passkeys for one site. rpID is its domain; origins are
// where the web UI runs, exactly.
func NewWebAuthn(rpID string, origins []string) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{RPDisplayName: "NepFleet", RPID: rpID, RPOrigins: origins})
}

// Ceremony kinds: an answer only finishes what it was started for.
const (
	kindSetup   = "setup"
	kindLogin   = "login"
	kindPasskey = "passkey"
)

// ceremony is a passkey exchange between its two requests.
type ceremony struct {
	kind    string
	addr    string
	data    webauthn.SessionData
	user    *auth.User // registering
	started time.Time
	expires time.Time
}

const ceremonyLifetime = 5 * time.Minute

// Ceremonies are bounded, but starting one is never refused: a refusal would let
// anyone block everyone's sign-in. Too many from one address replace that
// address's oldest; a full table replaces the oldest of all.
const (
	maxCeremonies        = 1000
	maxCeremoniesPerAddr = 8
)

// setupOK checks the one-time token, while setup is open.
func (a *Accounts) setupOK(token string) bool {
	a.mu.Lock()
	want := a.SetupToken
	a.mu.Unlock()
	return want != "" && !a.Users.Any() && subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1
}

func (a *Accounts) setupNeeded() bool {
	a.mu.Lock()
	want := a.SetupToken
	a.mu.Unlock()
	return want != "" && !a.Users.Any()
}

// addrKey groups an IPv6 client by its /64, as one host holds all of those.
func addrKey(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil || ip.To4() != nil {
		return addr
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}

func (a *Accounts) store(c ceremony) (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	c.addr = addrKey(c.addr)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ceremonies == nil {
		a.ceremonies = map[string]ceremony{}
	}
	now := time.Now()
	sameAddr, oldestSame, oldestAll := 0, "", ""
	for k, v := range a.ceremonies {
		if now.After(v.expires) {
			delete(a.ceremonies, k)
			continue
		}
		if oldestAll == "" || v.started.Before(a.ceremonies[oldestAll].started) {
			oldestAll = k
		}
		if v.addr == c.addr {
			sameAddr++
			if oldestSame == "" || v.started.Before(a.ceremonies[oldestSame].started) {
				oldestSame = k
			}
		}
	}
	switch {
	case sameAddr >= maxCeremoniesPerAddr:
		delete(a.ceremonies, oldestSame)
	case len(a.ceremonies) >= maxCeremonies:
		delete(a.ceremonies, oldestAll)
	}
	c.started, c.expires = now, now.Add(ceremonyLifetime)
	a.ceremonies[id] = c
	return id, nil
}

// take returns a ceremony once: a challenge can't be answered twice.
func (a *Accounts) take(id, kind string) (ceremony, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.ceremonies[id]
	delete(a.ceremonies, id)
	return c, ok && c.kind == kind && time.Now().Before(c.expires)
}

func registrationOptions() []webauthn.RegistrationOption {
	return []webauthn.RegistrationOption{
		// A PIN or biometric too: this is the only factor, and a session can do
		// everything. Selection first, as it replaces the whole setting.
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{UserVerification: protocol.VerificationRequired}),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
	}
}

func (a *Accounts) setCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: value, Path: "/api/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		// Browsers accept Secure on localhost too, so it's always on.
		Secure: true,
	})
}

func (a *Accounts) signIn(w http.ResponseWriter, u *auth.User) error {
	id, err := a.Sessions.Create(u)
	if err != nil {
		return err
	}
	a.setCookie(w, id, int(auth.SessionLifetime.Seconds()))
	return nil
}

// sessionToken is who a session cookie stands for, shaped like a token so all
// endpoints treat both alike. A person may do everything, on every stack.
func (a *Accounts) sessionToken(r *http.Request) (auth.Token, bool) {
	if a == nil {
		return auth.Token{}, false
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return auth.Token{}, false
	}
	uid, name, ok := a.Sessions.Check(c.Value)
	if !ok {
		return auth.Token{}, false
	}
	// Removing a user takes effect at once, not at the next restart.
	if _, err := a.Users.ByID(uid); err != nil {
		a.Sessions.End(c.Value)
		return auth.Token{}, false
	}
	return auth.Token{ID: "session", Name: name, Scope: auth.ScopeAdmin}, true
}

// sameOrigin stops other sites' pages from using a session: SameSite already
// keeps the cookie from their requests, and this holds if a browser doesn't.
func sameOrigin(r *http.Request, allowed []string) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	if slices.Contains(allowed, o) {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

// routes adds the sign-in endpoints. All failures read alike and count towards
// the lockout.
func (a *Accounts) routes(mux *http.ServeMux, l *limiter, o Options) {
	run := func(w http.ResponseWriter, r *http.Request, addr string, next func(http.ResponseWriter, *http.Request, string) error) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if !sameOrigin(r, o.AllowedOrigins) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		if err := next(w, r, addr); err != nil {
			l.fail(addr)
			log.Printf("api: sign-in: %v", err)
			http.Error(w, "sign-in failed", http.StatusUnauthorized)
		}
	}
	// guarded refuses a locked-out address first: for what can be guessed.
	guarded := func(next func(http.ResponseWriter, *http.Request, string) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			addr := clientAddr(r, o.TrustedProxies)
			if l.blocked(addr) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
				return
			}
			run(w, r, addr, next)
		}
	}
	// open always tries: a real passkey gets in from a locked-out address, like
	// a valid token, or noise from a shared address would block everyone's sign-in.
	// Passkeys can't be guessed, so nothing is lost.
	open := func(next func(http.ResponseWriter, *http.Request, string) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			run(w, r, clientAddr(r, o.TrustedProxies), next)
		}
	}
	mux.HandleFunc("GET /api/v1/auth/status", func(w http.ResponseWriter, r *http.Request) {
		t, ok := a.sessionToken(r)
		writeJSON(w, map[string]any{"setupNeeded": a.setupNeeded(), "signedIn": ok, "name": t.Name})
	})
	mux.HandleFunc("POST /api/v1/auth/setup/begin", guarded(func(w http.ResponseWriter, r *http.Request, addr string) error {
		var req struct {
			SetupToken string `json:"setupToken"`
			Name       string `json:"name"`
		}
		if !readJSON(w, r, &req) {
			return nil
		}
		if !a.setupOK(req.SetupToken) {
			return errors.New("wrong setup token, or setup is done")
		}
		u, err := auth.NewUser(strings.TrimSpace(req.Name))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return nil
		}
		options, data, err := a.WebAuthn.BeginRegistration(u, registrationOptions()...)
		if err != nil {
			return err
		}
		id, err := a.store(ceremony{kind: kindSetup, addr: addr, data: *data, user: u})
		if err != nil {
			return err
		}
		writeJSON(w, map[string]any{"ceremony": id, "options": options})
		return nil
	}))
	mux.HandleFunc("POST /api/v1/auth/setup/finish", guarded(func(w http.ResponseWriter, r *http.Request, _ string) error {
		c, ok := a.take(r.URL.Query().Get("ceremony"), kindSetup)
		// Setup must still be open now, not only when this started.
		if !ok || c.user == nil || !a.setupNeeded() {
			return errors.New("unknown ceremony, or setup is done")
		}
		cred, err := a.WebAuthn.FinishRegistration(c.user, c.data, r)
		if err != nil {
			return err
		}
		c.user.Credentials = []webauthn.Credential{*cred}
		if err := a.Users.Add(c.user, true); err != nil {
			return err
		}
		a.mu.Lock()
		a.SetupToken = ""
		a.mu.Unlock()
		log.Printf("api: first user %q created", c.user.Name)
		if err := a.signIn(w, c.user); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
	mux.HandleFunc("POST /api/v1/auth/login/begin", open(func(w http.ResponseWriter, _ *http.Request, addr string) error {
		// Discoverable: no name asked, so nothing tells which users exist.
		options, data, err := a.WebAuthn.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
		if err != nil {
			return err
		}
		id, err := a.store(ceremony{kind: kindLogin, addr: addr, data: *data})
		if err != nil {
			return err
		}
		writeJSON(w, map[string]any{"ceremony": id, "options": options})
		return nil
	}))
	mux.HandleFunc("POST /api/v1/auth/login/finish", open(func(w http.ResponseWriter, r *http.Request, _ string) error {
		c, ok := a.take(r.URL.Query().Get("ceremony"), kindLogin)
		if !ok {
			return errors.New("unknown ceremony")
		}
		var user *auth.User
		cred, err := a.WebAuthn.FinishDiscoverableLogin(func(_, userHandle []byte) (webauthn.User, error) {
			u, err := a.Users.ByID(userHandle)
			user = u
			return u, err
		}, c.data, r)
		if err != nil {
			return err
		}
		if cred.Authenticator.CloneWarning {
			return errors.New("passkey signature count went backwards: it may have been copied")
		}
		if err := a.Users.UpdateCredential(user.ID, *cred); err != nil {
			return err
		}
		log.Printf("api: %q signed in", user.Name)
		if err := a.signIn(w, user); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
	mux.HandleFunc("POST /api/v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r, o.AllowedOrigins) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		if c, err := r.Cookie(SessionCookie); err == nil {
			a.Sessions.End(c.Value)
		}
		a.setCookie(w, "", -1)
		w.WriteHeader(http.StatusNoContent)
	})
	// Another passkey (a second device) needs a person signed in, not a token.
	mux.HandleFunc("POST /api/v1/auth/passkeys/begin", guarded(func(w http.ResponseWriter, r *http.Request, addr string) error {
		c, err := r.Cookie(SessionCookie)
		if err != nil {
			return err
		}
		uid, _, ok := a.Sessions.Check(c.Value)
		if !ok {
			return errors.New("not signed in")
		}
		u, err := a.Users.ByID(uid)
		if err != nil {
			return err
		}
		exclude := make([]protocol.CredentialDescriptor, 0, len(u.Credentials))
		for _, cr := range u.Credentials {
			exclude = append(exclude, cr.Descriptor())
		}
		options, data, err := a.WebAuthn.BeginRegistration(u, append(registrationOptions(), webauthn.WithExclusions(exclude))...)
		if err != nil {
			return err
		}
		id, err := a.store(ceremony{kind: kindPasskey, addr: addr, data: *data, user: u})
		if err != nil {
			return err
		}
		writeJSON(w, map[string]any{"ceremony": id, "options": options})
		return nil
	}))
	mux.HandleFunc("POST /api/v1/auth/passkeys/finish", guarded(func(w http.ResponseWriter, r *http.Request, _ string) error {
		s, err := r.Cookie(SessionCookie)
		if err != nil {
			return err
		}
		uid, _, ok := a.Sessions.Check(s.Value)
		if !ok {
			return errors.New("not signed in")
		}
		c, ok := a.take(r.URL.Query().Get("ceremony"), kindPasskey)
		if !ok || c.user == nil || subtle.ConstantTimeCompare(c.user.ID, uid) != 1 {
			return errors.New("unknown ceremony")
		}
		cred, err := a.WebAuthn.FinishRegistration(c.user, c.data, r)
		if err != nil {
			return err
		}
		if err := a.Users.AddCredential(uid, *cred); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
}
