// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// ImageUpdate is what the registry has for one image of a stack, as
// `nepfleet updates --json` prints it.
type ImageUpdate struct {
	Stack   string `json:"stack"`
	Image   string `json:"image"`
	Track   string `json:"track"`
	Current string `json:"current"`
	// Next is the newest tag the track allows; it comes with a template
	// update (the template pins tags).
	Next string `json:"next,omitempty"`
	// Beyond is a newer tag outside the track, for information.
	Beyond string `json:"beyond,omitempty"`
	// Rebuilt: the current tag points at a newer digest than lock.yaml
	// pins; apply --update-lock takes it.
	Rebuilt bool   `json:"rebuilt,omitempty"`
	Error   string `json:"error,omitempty"`
}

func registerUpdates(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/stacks/{name}/updates", requireAuth(o, l, auth.ScopeRead, forStack(auth.ScopeRead, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.Updates == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		u, err := o.Updates(r.Context(), name)
		if writeErr(w, err) {
			return
		}
		if u == nil {
			u = []ImageUpdate{}
		}
		writeJSON(w, u)
	})))
}
