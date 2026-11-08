// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

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

func registerEgress(mux *http.ServeMux, o Options, l *limiter) {
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
