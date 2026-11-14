// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// TokenInfo is a stored API token as the API shows it: never its secret or hash.
type TokenInfo struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Scope   string    `json:"scope"`
	Stacks  []string  `json:"stacks,omitempty"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	Expired bool      `json:"expired"`
}

// NewToken is what POST /api/v1/tokens takes, as nepfleet token create.
type NewToken struct {
	Name   string   `json:"name"`
	Scope  string   `json:"scope"`
	Stacks []string `json:"stacks,omitempty"`
	// Days until it expires: 1 to 3650.
	Days int `json:"days"`
}

// CreatedToken carries the secret, shown this once.
type CreatedToken struct {
	Token  TokenInfo `json:"token"`
	Secret string    `json:"secret"`
}

var tokenID = regexp.MustCompile(`^[0-9a-f]{8}$`)

func tokenInfo(t auth.Token, now time.Time) TokenInfo {
	return TokenInfo{ID: t.ID, Name: t.Name, Scope: t.Scope, Stacks: t.Stacks, Created: t.Created, Expires: t.Expires, Expired: !now.Before(t.Expires)}
}

// mayChangeAccess answers a session without a recent verification, as
// changing who may do what is worth a passkey, like editing stacks.
func mayChangeAccess(w http.ResponseWriter, r *http.Request, o Options) bool {
	t, _ := TokenFrom(r.Context())
	if t.ID == "session" && !o.Accounts.recentlyVerified(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"verify":true,"message":"confirm it's you with a passkey, then try again"}` + "\n"))
		return false
	}
	return true
}

func registerTokens(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/tokens", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, _ *http.Request) {
		all, err := o.Tokens.List()
		if writeErr(w, err) {
			return
		}
		now := time.Now()
		out := make([]TokenInfo, 0, len(all))
		for _, t := range all {
			out = append(out, tokenInfo(t, now))
		}
		writeJSON(w, out)
	}))
	mux.HandleFunc("POST /api/v1/tokens", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if !mayChangeAccess(w, r, o) {
			return
		}
		var req NewToken
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || len(req.Name) > 64 || len(req.Stacks) > 100 {
			http.Error(w, `send {"name", "scope", "stacks", "days"}`, http.StatusBadRequest)
			return
		}
		if req.Days < 1 || req.Days > 3650 {
			http.Error(w, "days is 1 to 3650", http.StatusUnprocessableEntity)
			return
		}
		for _, s := range req.Stacks {
			if !stackName.MatchString(s) {
				http.Error(w, s+" is not a stack name", http.StatusUnprocessableEntity)
				return
			}
		}
		secret, t, err := o.Tokens.Create(req.Name, req.Scope, req.Stacks, time.Duration(req.Days)*24*time.Hour)
		if err != nil {
			// The store's reasons name the rule broken, nothing internal.
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		by, _ := TokenFrom(r.Context())
		log.Printf("api: %q created token %s (%q, %s) (token %s)", by.Name, t.ID, t.Name, t.Scope, by.ID) //nolint:gosec // %q escapes the names
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(CreatedToken{Token: tokenInfo(t, time.Now()), Secret: secret})
	}))
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if !mayChangeAccess(w, r, o) {
			return
		}
		id := r.PathValue("id")
		if !tokenID.MatchString(id) {
			http.Error(w, ErrNotFound.Error(), http.StatusNotFound)
			return
		}
		if err := o.Tokens.Revoke(id); err != nil {
			http.Error(w, "no such token", http.StatusNotFound)
			return
		}
		by, _ := TokenFrom(r.Context())
		log.Printf("api: %q revoked token %s (token %s)", by.Name, id, by.ID) //nolint:gosec // %q escapes the name
		w.WriteHeader(http.StatusNoContent)
	}))
}
