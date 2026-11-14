// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// NewStack creates a stack from a template, as nepfleet new does.
type NewStack struct {
	Template string `json:"template"`
	// Source is a template source from fleet.yaml; default official.
	Source string `json:"source,omitempty"`
	// Name defaults to the template's name.
	Name string `json:"name,omitempty"`
	// Target is the host; default local.
	Target string `json:"target,omitempty"`
	// Access is domain (behind Caddy, the default) or port (a LAN port).
	Access string `json:"access,omitempty"`
	// Port for access port; 0 picks the lowest free one in the host's range.
	Port int `json:"port,omitempty"`
	// Values are the template's inputs. Secret ones are stored encrypted and
	// never returned.
	Values map[string]string `json:"values,omitempty"`
}

// NewStackResult is the stack created, committed, not yet applied.
type NewStackResult struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
	Port   int    `json:"port,omitempty"`
}

// ErrStackExists means a stack of that name is there already.
var ErrStackExists = errors.New("a stack of that name exists already")

func registerNewStack(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("POST /api/v1/stacks", requireAuth(o, l, auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request) {
		if o.NewStack == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		t, _ := TokenFrom(r.Context())
		if t.ID == "session" && !o.Accounts.recentlyVerified(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"verify":true,"message":"confirm it's you with a passkey, then create it again"}` + "\n"))
			return
		}
		var req NewStack
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.Template == "" || len(req.Values) > 200 {
			http.Error(w, `send {"template": "<name>", "name", "target", "access", "port", "values": {"INPUT": "value"}}`, http.StatusBadRequest)
			return
		}
		// A token limited to some stacks creates only those.
		name := req.Name
		if name == "" {
			name = req.Template
		}
		if len(t.Stacks) > 0 && !slices.Contains(t.Stacks, name) {
			http.Error(w, fmt.Sprintf("this token may not create %s", name), http.StatusForbidden)
			return
		}
		res, err := o.NewStack(r.Context(), req, t.Name)
		var invalid InvalidError
		switch {
		case errors.Is(err, ErrStackExists):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case errors.As(err, &invalid):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		case errors.Is(err, ErrBusy):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case writeErr(w, err):
			return
		}
		log.Printf("api: %s created stack %s from %s in %s (token %s)", t.Name, res.Name, req.Template, res.Commit, t.ID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(res)
	}))
}
