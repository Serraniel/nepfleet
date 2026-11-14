// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"strings"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// Feature is an opt-in part of NepFleet run as a system stack from the
// official catalogue, as nepfleet enable lists it. Enabling one is creating a
// stack from its template (POST /api/v1/stacks).
type Feature struct {
	Name     string `json:"name"`
	Template string `json:"template"`
	What     string `json:"what"`
	// Stacks already provide it, as name@target (only those the token sees).
	Stacks []string `json:"stacks"`
}

func registerFeatures(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/features", requireAuth(o, l, auth.ScopeRead, func(w http.ResponseWriter, r *http.Request) {
		if o.Features == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		fs, err := o.Features()
		if writeErr(w, err) {
			return
		}
		t, _ := TokenFrom(r.Context())
		out := make([]Feature, 0, len(fs))
		for _, f := range fs {
			seen := []string{}
			for _, s := range f.Stacks {
				name, _, _ := strings.Cut(s, "@")
				if t.Allows(auth.ScopeRead, name) {
					seen = append(seen, s)
				}
			}
			f.Stacks = seen
			out = append(out, f)
		}
		writeJSON(w, out)
	}))
}
