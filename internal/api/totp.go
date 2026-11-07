// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// A code is six digits and every session can do everything, so guessing has to
// stay out of reach from many addresses too: failures also count per user. That
// lets anyone block a user's codes for a while, but not their passkeys.
const (
	totpMaxFails = 5
	totpLockout  = 15 * time.Minute
)

type totpFails struct {
	n     int
	until time.Time
}

func (a *Accounts) now() time.Time {
	if a.Sessions != nil && a.Sessions.Now != nil {
		return a.Sessions.Now()
	}
	return time.Now()
}

func (a *Accounts) totpLocked(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.now().Before(a.totpFails[name].until)
}

// totpFailed counts a failure, only for users that exist: the table stays bounded.
func (a *Accounts) totpFailed(name string) {
	all, err := a.Users.List()
	if err != nil || !slices.ContainsFunc(all, func(u auth.User) bool { return u.Name == name }) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.totpFails == nil {
		a.totpFails = map[string]totpFails{}
	}
	f := a.totpFails[name]
	if !f.until.IsZero() && a.now().After(f.until) {
		f = totpFails{}
	}
	f.n++
	if f.n >= totpMaxFails {
		f.until = a.now().Add(totpLockout)
	}
	a.totpFails[name] = f
}

func (a *Accounts) totpSucceeded(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.totpFails, name)
}

// signedInUser is the user behind a request's session.
func (a *Accounts) signedInUser(r *http.Request) (*auth.User, error) {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil, err
	}
	uid, _, ok := a.Sessions.Check(c.Value)
	if !ok {
		return nil, errors.New("not signed in")
	}
	return a.Users.ByID(uid)
}

// totpOn says whether the signed-in user has codes on. Nobody else learns it.
func (a *Accounts) totpOn(r *http.Request) bool {
	u, err := a.signedInUser(r)
	return err == nil && u.TOTP != nil
}

// isExternal says whether the signed-in user came through an identity provider.
func (a *Accounts) isExternal(r *http.Request) bool {
	u, err := a.signedInUser(r)
	return err == nil && u.External
}

type guardedRoute = func(func(http.ResponseWriter, *http.Request, string) error) http.HandlerFunc

func (a *Accounts) totpRoutes(mux *http.ServeMux, guarded guardedRoute) {
	// Turning codes on takes a session and one good code, so a misread secret
	// can't lock anyone out of the fallback they count on.
	mux.HandleFunc("POST /api/v1/auth/totp/begin", guarded(func(w http.ResponseWriter, r *http.Request, addr string) error {
		u, err := a.signedInUser(r)
		if err != nil {
			return err
		}
		if u.External {
			return errors.New("people from an identity provider sign in there")
		}
		secret, err := auth.NewTOTPSecret()
		if err != nil {
			return err
		}
		id, err := a.store(ceremony{kind: kindTOTP, addr: addr, user: u, secret: secret})
		if err != nil {
			return err
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, map[string]any{"ceremony": id, "secret": secret, "url": auth.TOTPURL(u.Name, secret)})
		return nil
	}))
	mux.HandleFunc("POST /api/v1/auth/totp/finish", guarded(func(w http.ResponseWriter, r *http.Request, _ string) error {
		u, err := a.signedInUser(r)
		if err != nil {
			return err
		}
		var req struct {
			Code string `json:"code"`
		}
		if !readJSON(w, r, &req) {
			return nil
		}
		id := r.URL.Query().Get("ceremony")
		c, ok := a.take(id, kindTOTP)
		if !ok || c.user == nil || string(c.user.ID) != string(u.ID) {
			return errors.New("unknown ceremony")
		}
		step, ok := auth.CheckTOTPCode(c.secret, strings.TrimSpace(req.Code), a.now())
		if !ok {
			// A mistyped code shouldn't mean scanning a new secret.
			a.mu.Lock()
			a.ceremonies[id] = c
			a.mu.Unlock()
			return errors.New("wrong code for a new authenticator")
		}
		if err := a.Users.SetTOTP(u.ID, &auth.TOTP{Secret: c.secret, LastStep: step}); err != nil {
			return err
		}
		log.Printf("api: %q turned on sign-in codes", u.Name)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
	mux.HandleFunc("POST /api/v1/auth/totp/remove", guarded(func(w http.ResponseWriter, r *http.Request, _ string) error {
		u, err := a.signedInUser(r)
		if err != nil {
			return err
		}
		if err := a.Users.SetTOTP(u.ID, nil); err != nil {
			return err
		}
		log.Printf("api: %q turned off sign-in codes", u.Name)
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
	// Guarded: unlike a passkey, a code can be guessed. All failures read alike,
	// so this doesn't tell which users exist or have codes on.
	mux.HandleFunc("POST /api/v1/auth/totp/login", guarded(func(w http.ResponseWriter, r *http.Request, _ string) error {
		var req struct {
			Name string `json:"name"`
			Code string `json:"code"`
		}
		if !readJSON(w, r, &req) {
			return nil
		}
		name := strings.TrimSpace(req.Name)
		if a.totpLocked(name) {
			return errors.New("too many wrong codes for this user")
		}
		u, err := a.Users.VerifyTOTP(name, strings.TrimSpace(req.Code), a.now())
		if err != nil {
			a.totpFailed(name)
			return err
		}
		a.totpSucceeded(name)
		log.Printf("api: %q signed in with a code", u.Name)
		if err := a.signIn(w, u); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}))
}
