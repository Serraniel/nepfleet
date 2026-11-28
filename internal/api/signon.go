// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// Sign-on (docs/sign-on.md): fleet.yaml's spec.signOn names the fleet's
// identity provider, a stack; each stack's spec.signOn.mode says how its
// users sign in.

// SignOnSettings are GET /api/v1/signon: the fleet's provider and which
// stacks could be one or use it.
type SignOnSettings struct {
	// Provider is the provider stack's name; "" without one.
	Provider string `json:"provider"`
	// Kinds are the templates NepFleet can register clients in.
	Kinds []string `json:"kinds"`
	// Candidates are the stacks running one of Kinds.
	Candidates []string `json:"candidates"`
	// Stacks are those whose template supports sign-on, or that set a mode.
	Stacks []StackSignOnMode `json:"stacks"`
}

// StackSignOnMode is how one stack signs in.
type StackSignOnMode struct {
	Name string `json:"name"`
	// Mode is the one in effect: integrated, custom or none.
	Mode string `json:"mode"`
	// Explicit: stack.yaml sets the mode; otherwise it's the default.
	Explicit bool `json:"explicit"`
}

// SignOnUpdate is what PUT /api/v1/signon takes.
type SignOnUpdate struct {
	Provider string `json:"provider"`
}

// StackSignOn is GET /api/v1/stacks/{name}/signon.
type StackSignOn struct {
	// Supported: the stack's template declares spec.oidc.
	Supported bool `json:"supported"`
	// Mode is the one in effect; Set is stack.yaml's own ("" for the default).
	Mode string `json:"mode"`
	Set  string `json:"set"`
	// Issuer and ClientID are stack.yaml's, for mode custom.
	Issuer   string `json:"issuer,omitempty"`
	ClientID string `json:"clientID,omitempty"`
	// Provider is the fleet's provider stack; "" without one.
	Provider string `json:"provider"`
	// SecretSet: NEPFLEET_OIDC_CLIENT_SECRET is stored (edit scope only).
	SecretSet *bool `json:"secretSet,omitempty"`
}

// StackSignOnUpdate is what PUT /api/v1/stacks/{name}/signon takes. Mode ""
// removes spec.signOn, so the default applies. The custom client secret goes
// through PUT /api/v1/stacks/{name}/secrets/NEPFLEET_OIDC_CLIENT_SECRET.
type StackSignOnUpdate struct {
	Mode     string `json:"mode"`
	Issuer   string `json:"issuer,omitempty"`
	ClientID string `json:"clientID,omitempty"`
}

// ErrNoSignOn means fleet.yaml names no provider.
var ErrNoSignOn = errors.New("no sign-on provider in fleet.yaml")

var signOnModes = []string{"", "integrated", "custom", "none"}

func registerSignOn(mux *http.ServeMux, o Options, l *limiter) {
	verified := func(w http.ResponseWriter, r *http.Request) bool {
		t, _ := TokenFrom(r.Context())
		if t.ID == "session" && !o.Accounts.recentlyVerified(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"verify":true,"message":"confirm it's you with a passkey, then save again"}` + "\n"))
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/v1/signon", requireAuth(o, l, auth.ScopeRead, func(w http.ResponseWriter, r *http.Request) {
		if o.SignOn == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		s, err := o.SignOn(r.Context())
		if writeErr(w, err) {
			return
		}
		// Only the stacks this token sees.
		t, _ := TokenFrom(r.Context())
		s.Candidates = slices.DeleteFunc(slices.Clone(s.Candidates), func(n string) bool { return !t.Allows(auth.ScopeRead, n) })
		s.Stacks = slices.DeleteFunc(slices.Clone(s.Stacks), func(m StackSignOnMode) bool { return !t.Allows(auth.ScopeRead, m.Name) })
		if s.Kinds == nil {
			s.Kinds = []string{}
		}
		if s.Candidates == nil {
			s.Candidates = []string{}
		}
		if s.Stacks == nil {
			s.Stacks = []StackSignOnMode{}
		}
		writeJSON(w, s)
	}))
	// The provider signs users in to every stack: admin, as fleet-wide settings.
	mux.HandleFunc("PUT /api/v1/signon", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.SetSignOn == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		if !verified(w, r) {
			return
		}
		var req SignOnUpdate
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || !stackName.MatchString(req.Provider) {
			http.Error(w, `send {"provider": "<stack>"}`, http.StatusBadRequest)
			return
		}
		t, _ := TokenFrom(r.Context())
		commit, err := o.SetSignOn(r.Context(), req.Provider, t.Name)
		if signOnErr(w, err) {
			return
		}
		logf("api: %q set the sign-on provider to %s in %s (token %s)", t.Name, req.Provider, commit, t.ID)
		writeJSON(w, SecretResult{Commit: commit})
	}))
	mux.HandleFunc("DELETE /api/v1/signon", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.RemoveSignOn == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		if !verified(w, r) {
			return
		}
		t, _ := TokenFrom(r.Context())
		commit, err := o.RemoveSignOn(r.Context(), t.Name)
		if signOnErr(w, err) {
			return
		}
		logf("api: %q removed the sign-on provider in %s (token %s)", t.Name, commit, t.ID)
		writeJSON(w, SecretResult{Commit: commit})
	}))
	mux.HandleFunc("GET /api/v1/stacks/{name}/signon", requireAuth(o, l, auth.ScopeRead, forStack(auth.ScopeRead, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.StackSignOn == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		s, err := o.StackSignOn(r.Context(), name)
		if writeErr(w, err) {
			return
		}
		// Secret keys are for edit scope, as GET …/secrets.
		if t, _ := TokenFrom(r.Context()); !t.Allows(auth.ScopeEdit, name) {
			s.SecretSet = nil
		}
		writeJSON(w, s)
	})))
	mux.HandleFunc("PUT /api/v1/stacks/{name}/signon", requireAuth(o, l, auth.ScopeEdit, forStack(auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.SetStackSignOn == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		if !verified(w, r) {
			return
		}
		var req StackSignOnUpdate
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || !slices.Contains(signOnModes, req.Mode) {
			http.Error(w, `send {"mode": "integrated|custom|none|", "issuer": "…", "clientID": "…"}`, http.StatusBadRequest)
			return
		}
		if envUnsafe(req.Issuer) || envUnsafe(req.ClientID) {
			http.Error(w, "issuer and clientID can't contain a single quote or a line break", http.StatusBadRequest)
			return
		}
		t, _ := TokenFrom(r.Context())
		commit, err := o.SetStackSignOn(r.Context(), name, req, t.Name)
		if signOnErr(w, err) {
			return
		}
		logf("api: %q set the sign-on of %s to %q in %s (token %s)", t.Name, name, req.Mode, commit, t.ID)
		writeJSON(w, SecretResult{Commit: commit})
	})))
}

// signOnErr answers a sign-on change's error.
func signOnErr(w http.ResponseWriter, err error) bool {
	var invalid InvalidError
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNoSignOn), errors.Is(err, ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrBusy), errors.Is(err, ErrUncommitted):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &invalid):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	default:
		logf("api: sign-on: %v", err)
		http.Error(w, "the sign-on settings couldn't be saved; see the server log", http.StatusInternalServerError)
	}
	return true
}
