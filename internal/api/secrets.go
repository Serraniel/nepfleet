// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// Secrets are write-only over the API: a client sees their keys, never their
// values, as nepfleet secret set does on the command line. A stack's go in its
// values.secret.yaml (edit scope); the fleet-wide ones in fleet.secret.yaml
// (admin scope, as every stack may read them).

// SecretKeys are the keys a stack's, or the fleet's, encrypted file holds.
type SecretKeys struct {
	Keys []string `json:"keys"`
}

// SecretValue is what PUT …/secrets/{key} takes.
type SecretValue struct {
	Value string `json:"value"`
}

// SecretResult is the commit that stored, or removed, a secret.
type SecretResult struct {
	Commit string `json:"commit"`
}

// ErrNoSecret means there is no secret of that key to remove.
var ErrNoSecret = errors.New("no secret of that key")

// ErrUncommitted means the file has changes on the server that aren't
// committed: they stay, and nothing is saved.
var ErrUncommitted = errors.New("the secrets file has uncommitted changes on the server; commit or discard them first")

// secretKey is a name a compose file can use as a variable.
var secretKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// maxSecret bounds a value: a certificate or key fits, a file doesn't belong.
const maxSecret = 64 << 10

func registerSecrets(mux *http.ServeMux, o Options, l *limiter) {
	keys := func(w http.ResponseWriter, r *http.Request, stack string) {
		if o.SecretKeys == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		ks, err := o.SecretKeys(r.Context(), stack)
		if writeErr(w, err) {
			return
		}
		if ks == nil {
			ks = []string{}
		}
		writeJSON(w, SecretKeys{Keys: ks})
	}
	change := func(w http.ResponseWriter, r *http.Request, stack string, remove bool) {
		if o.SetSecret == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		key := r.PathValue("key")
		if !secretKey.MatchString(key) || key == "sops" {
			http.Error(w, "a key is a variable name: letters, digits and _, not starting with a digit (and not sops)", http.StatusBadRequest)
			return
		}
		t, _ := TokenFrom(r.Context())
		if t.ID == "session" && !o.Accounts.recentlyVerified(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"verify":true,"message":"confirm it's you with a passkey, then save again"}` + "\n"))
			return
		}
		var value *string
		if !remove {
			var v SecretValue
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSecret+1024))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&v); err != nil || len(v.Value) > maxSecret {
				http.Error(w, `send {"value": "…"} (at most 64 KiB)`, http.StatusBadRequest)
				return
			}
			value = &v.Value
		}
		commit, err := o.SetSecret(r.Context(), stack, key, value, t.Name)
		switch {
		case errors.Is(err, ErrNoSecret):
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		case errors.Is(err, ErrBusy), errors.Is(err, ErrUncommitted):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case writeErr(w, err):
			return
		}
		where := stack
		if where == "" {
			where = "the fleet"
		}
		verb := "set"
		if remove {
			verb = "removed"
		}
		// The key only: never the value.
		log.Printf("api: %q %s secret %s of %s in %s (token %s)", t.Name, verb, key, where, commit, t.ID) //nolint:gosec // %q escapes the name; checked key and stack names
		writeJSON(w, SecretResult{Commit: commit})
	}

	mux.HandleFunc("GET /api/v1/stacks/{name}/secrets", requireAuth(o, l, auth.ScopeEdit, forStack(auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request, name string) {
		keys(w, r, name)
	})))
	mux.HandleFunc("PUT /api/v1/stacks/{name}/secrets/{key}", requireAuth(o, l, auth.ScopeEdit, forStack(auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request, name string) {
		change(w, r, name, false)
	})))
	mux.HandleFunc("DELETE /api/v1/stacks/{name}/secrets/{key}", requireAuth(o, l, auth.ScopeEdit, forStack(auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request, name string) {
		change(w, r, name, true)
	})))
	mux.HandleFunc("GET /api/v1/secrets", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		keys(w, r, "")
	}))
	mux.HandleFunc("PUT /api/v1/secrets/{key}", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		change(w, r, "", false)
	}))
	mux.HandleFunc("DELETE /api/v1/secrets/{key}", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		change(w, r, "", true)
	}))
}
