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

// StackEgress is what a stack's egress sidecars recorded (mode log) or refused
// (mode enforce), less what's already allowed, as `nepfleet egress suggest`.
type StackEgress struct {
	// Mode is off, log or enforce.
	Mode string `json:"mode"`
	// Sidecars counts the stack's egress sidecars on the server's host; none
	// means nothing is recorded.
	Sidecars int `json:"sidecars"`
	// Hosts are names looked up and then connected to (or refused).
	Hosts []string `json:"hosts"`
	// Addresses were connected to (or refused) with no name seen.
	Addresses []string `json:"addresses"`
	// LookedUp are names looked up but never connected to.
	LookedUp []string `json:"lookedUp"`
}

// egressEntry is the shape of a host name, an address or a CIDR range; the
// stack's validation checks the rest.
var egressEntry = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,253}$`)

// EgressAllow is what POST …/egress/allow takes: host names, addresses or
// CIDR ranges to add to the stack's spec.egress.allow.
type EgressAllow struct {
	Add []string `json:"add"`
}

// EgressAllowResult is the commit that changed stack.yaml (empty when all were
// allowed already) and what it added.
type EgressAllowResult struct {
	Commit string   `json:"commit,omitempty"`
	Added  []string `json:"added"`
}

func registerEgress(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("POST /api/v1/stacks/{name}/egress/allow", requireAuth(o, l, auth.ScopeEdit, forStack(auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.EgressAllow == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		if !mayChangeAccess(w, r, o) {
			return
		}
		var req EgressAllow
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || len(req.Add) == 0 || len(req.Add) > 200 {
			http.Error(w, `send {"add": ["host.example.org", "203.0.113.7"]}`, http.StatusBadRequest)
			return
		}
		for _, a := range req.Add {
			if !egressEntry.MatchString(a) {
				http.Error(w, a+" is not a host name, address or range", http.StatusUnprocessableEntity)
				return
			}
		}
		t, _ := TokenFrom(r.Context())
		res, err := o.EgressAllow(r.Context(), name, req.Add, t.Name)
		var invalid InvalidError
		switch {
		case errors.As(err, &invalid):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		case errors.Is(err, ErrBusy), errors.Is(err, ErrUncommitted):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case writeErr(w, err):
			return
		}
		if res.Commit != "" {
			log.Printf("api: %q allowed egress of %s to %d more (token %s)", t.Name, name, len(res.Added), t.ID) //nolint:gosec // %q escapes the name
		}
		writeJSON(w, res)
	})))
	mux.HandleFunc("GET /api/v1/stacks/{name}/egress", requireAuth(o, l, auth.ScopeRead, forStack(auth.ScopeRead, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.Egress == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		e, err := o.Egress(r.Context(), name)
		if writeErr(w, err) {
			return
		}
		writeJSON(w, e)
	})))
}
