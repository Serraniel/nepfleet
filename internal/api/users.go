// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/base64"
	"log"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// UserInfo is a person who signs in, as nepfleet user list shows them.
type UserInfo struct {
	Name     string        `json:"name"`
	Created  time.Time     `json:"created"`
	External bool          `json:"external,omitempty"`
	Codes    bool          `json:"codes"`
	Passkeys []PasskeyInfo `json:"passkeys"`
}

// PasskeyInfo is one of a person's passkeys. ID is its whole credential ID
// (base64url, public), Name its start as nepfleet user passkeys shows it.
type PasskeyInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Synced bool   `json:"synced"`
	Uses   uint32 `json:"uses"`
	// Locked passkeys may have been copied and are refused until removed.
	Locked bool `json:"locked,omitempty"`
}

var passkeyID = regexp.MustCompile(`^[A-Za-z0-9_-]{4,}$`)

func registerUsers(mux *http.ServeMux, o Options, l *limiter) {
	accounts := func(w http.ResponseWriter) bool {
		if o.Accounts == nil || o.Accounts.Users == nil {
			http.Error(w, "this server has no accounts: start serve with passkeys or OIDC", http.StatusServiceUnavailable)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/v1/users", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, _ *http.Request) {
		if !accounts(w) {
			return
		}
		all, err := o.Accounts.Users.List()
		if writeErr(w, err) {
			return
		}
		out := make([]UserInfo, 0, len(all))
		for _, u := range all {
			ui := UserInfo{Name: u.Name, Created: u.Created, External: u.External, Codes: u.TOTP != nil, Passkeys: []PasskeyInfo{}}
			for _, c := range u.Credentials {
				ui.Passkeys = append(ui.Passkeys, PasskeyInfo{ID: base64.RawURLEncoding.EncodeToString(c.ID), Name: auth.CredentialName(c.ID), Synced: c.Flags.BackupEligible, Uses: c.Authenticator.SignCount, Locked: u.IsLocked(c.ID)})
			}
			out = append(out, ui)
		}
		writeJSON(w, out)
	}))
	mux.HandleFunc("DELETE /api/v1/users/{user}", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if !accounts(w) || !mayChangeAccess(w, r, o) {
			return
		}
		name := r.PathValue("user")
		t, _ := TokenFrom(r.Context())
		if t.ID == "session" && t.Name == name {
			http.Error(w, "you can't remove yourself here: another person, or nepfleet user remove on the server, can", http.StatusConflict)
			return
		}
		all, err := o.Accounts.Users.List()
		if writeErr(w, err) {
			return
		}
		if !slices.ContainsFunc(all, func(u auth.User) bool { return u.Name == name }) {
			http.Error(w, "no such user", http.StatusNotFound)
			return
		}
		if err := o.Accounts.Users.Remove(name); writeErr(w, err) {
			return
		}
		log.Printf("api: %q removed user %q (token %s)", t.Name, name, t.ID) //nolint:gosec // %q escapes the names
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("DELETE /api/v1/users/{user}/passkeys/{id}", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if !accounts(w) || !mayChangeAccess(w, r, o) {
			return
		}
		name, id := r.PathValue("user"), r.PathValue("id")
		if !passkeyID.MatchString(id) {
			http.Error(w, "name the passkey by the start of its ID", http.StatusBadRequest)
			return
		}
		if err := o.Accounts.Users.RemoveCredential(name, id); err != nil {
			// The store's reasons: no such user or passkey, ambiguous, or the last way in.
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		t, _ := TokenFrom(r.Context())
		log.Printf("api: %q removed passkey %s of %q (token %s)", t.Name, id, name, t.ID) //nolint:gosec // %q escapes the names; a checked ID
		w.WriteHeader(http.StatusNoContent)
	}))
}
