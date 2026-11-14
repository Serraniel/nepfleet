// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"net/http"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// RenderedFile is one file a stack renders to, secrets masked.
type RenderedFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func registerRender(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/stacks/{name}/render", requireAuth(o, l, auth.ScopeRead, forStack(auth.ScopeRead, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.Render == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		files, err := o.Render(r.Context(), name)
		var invalid InvalidError
		if errors.As(err, &invalid) {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if writeErr(w, err) {
			return
		}
		if files == nil {
			files = []RenderedFile{}
		}
		writeJSON(w, files)
	})))
}
