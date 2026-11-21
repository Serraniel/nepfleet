// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// A stack's inputs are what its template asks for: shown with whether each is
// set (never a value), and set together in one commit. Secret ones go into the
// stack's encrypted values.secret.yaml, the others into stack.yaml, so a
// client needn't know which is which. This is how a client fills in what a
// newer template version asks for (the plan says "X is required").

// StackInput is one input of a stack's template, and whether it is set.
type StackInput struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	// Secret: kept encrypted, written but never read back.
	Secret bool `json:"secret,omitempty"`
	// Set: the stack has a value of its own (secret or not).
	Set bool `json:"set,omitempty"`
	// Value is a non-secret input's current value, "" for secrets.
	Value string `json:"value,omitempty"`
	// Missing: required, without a default, and not set: the stack can't be
	// planned until it is.
	Missing bool `json:"missing,omitempty"`
}

// StackInputs is GET …/inputs.
type StackInputs struct {
	Inputs []StackInput `json:"inputs"`
}

// InputValues is what PUT …/inputs takes: input name → value. An empty value
// leaves that input as it is.
type InputValues struct {
	Values map[string]string `json:"values"`
}

// maxInputs bounds a PUT: every value at most a secret's size.
const maxInputs = 256 << 10

func registerInputs(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/stacks/{name}/inputs", requireAuth(o, l, auth.ScopeRead, forStack(auth.ScopeRead, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.StackInputs == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		ins, err := o.StackInputs(r.Context(), name)
		if writeErr(w, err) {
			return
		}
		if ins == nil {
			ins = []StackInput{}
		}
		writeJSON(w, StackInputs{Inputs: ins})
	})))
	mux.HandleFunc("PUT /api/v1/stacks/{name}/inputs", requireAuth(o, l, auth.ScopeEdit, forStack(auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.SetInputs == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		t, _ := TokenFrom(r.Context())
		if t.ID == "session" && !o.Accounts.recentlyVerified(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"verify":true,"message":"confirm it's you with a passkey, then save again"}` + "\n"))
			return
		}
		var in InputValues
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxInputs))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || len(in.Values) == 0 {
			http.Error(w, `send {"values": {"NAME": "value", …}}`, http.StatusBadRequest)
			return
		}
		for k, v := range in.Values {
			if !secretKey.MatchString(k) || len(v) > maxSecret {
				http.Error(w, "an input name is a variable name, and a value at most 64 KiB", http.StatusBadRequest)
				return
			}
		}
		commit, err := o.SetInputs(r.Context(), name, in.Values, t.Name)
		switch {
		case errors.Is(err, ErrBusy), errors.Is(err, ErrUncommitted):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case writeErr(w, err):
			return
		}
		names := make([]string, 0, len(in.Values))
		for k := range in.Values {
			names = append(names, k)
		}
		// The names only: never a value.
		logf("api: %q set inputs %v of %s in %s (token %s)", t.Name, names, name, commit, t.ID)
		writeJSON(w, SecretResult{Commit: commit})
	})))
}
