// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// ComposeProject is a Compose project the server's Docker knows, as nepfleet
// import compose lists them. Stack names the stack it is already, if any.
type ComposeProject struct {
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	ConfigFiles []string `json:"configFiles"`
	Stack       bool     `json:"stack"`
}

// ImportCompose imports one project as a stack and, as it runs here, records
// its containers as applied (the first plan is empty).
type ImportCompose struct {
	Project    string `json:"project"`
	EncryptAll bool   `json:"encryptAll,omitempty"`
}

// ImportResult is the stack created, committed, and what to know about it.
type ImportResult struct {
	Stack  string `json:"stack"`
	Commit string `json:"commit"`
	// Log is what import compose prints: values, encrypted secrets, volumes, warnings.
	Log string `json:"log"`
}

func registerImport(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/import/compose", requireAuth(o, l, auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request) {
		if o.ComposeProjects == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		// Every project and its paths on the host: not for a token limited to stacks.
		if t, _ := TokenFrom(r.Context()); len(t.Stacks) > 0 {
			http.Error(w, "listing the host's compose projects needs a token not limited to stacks", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		ps, err := o.ComposeProjects(ctx)
		if writeErr(w, err) {
			return
		}
		if ps == nil {
			ps = []ComposeProject{}
		}
		writeJSON(w, ps)
	}))
	mux.HandleFunc("POST /api/v1/import/compose", requireAuth(o, l, auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request) {
		if o.ImportCompose == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		t, _ := TokenFrom(r.Context())
		if len(t.Stacks) > 0 {
			http.Error(w, "importing creates a stack: this needs a token not limited to stacks", http.StatusForbidden)
			return
		}
		if !mayChangeAccess(w, r, o) {
			return
		}
		var req ImportCompose
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || !stackName.MatchString(req.Project) {
			http.Error(w, `send {"project": "<compose project name>"}`, http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		res, err := o.ImportCompose(ctx, req, t.Name)
		var invalid InvalidError
		switch {
		case errors.Is(err, ErrStackExists), errors.Is(err, ErrBusy):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case errors.As(err, &invalid):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		case writeErr(w, err):
			return
		}
		log.Printf("api: %q imported compose project %s in %s (token %s)", t.Name, req.Project, res.Commit, t.ID) //nolint:gosec // %q escapes the name; a checked project name
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(res)
	}))
}
