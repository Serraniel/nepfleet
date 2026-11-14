// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// The SBOM endpoints answer CycloneDX JSON, as nepfleet sbom writes it.
const cycloneDX = "application/vnd.cyclonedx+json"

func registerSBOM(mux *http.ServeMux, o Options, l *limiter) {
	answer := func(w http.ResponseWriter, r *http.Request, stack, file string) {
		if o.SBOM == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		t, _ := TokenFrom(r.Context())
		allow := func(s string) bool { return t.Allows(auth.ScopeRead, s) }
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		doc, err := o.SBOM(ctx, stack, allow, r.URL.Query().Get("contents") == "true")
		if writeErr(w, err) {
			return
		}
		w.Header().Set("Content-Type", cycloneDX)
		w.Header().Set("Content-Disposition", `attachment; filename="`+file+`.cdx.json"`)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false) // package URLs hold &
		_ = enc.Encode(doc)
	}
	mux.HandleFunc("GET /api/v1/stacks/{name}/sbom", requireAuth(o, l, auth.ScopeRead, forStack(auth.ScopeRead, func(w http.ResponseWriter, r *http.Request, name string) {
		answer(w, r, name, name)
	})))
	mux.HandleFunc("GET /api/v1/sbom", requireAuth(o, l, auth.ScopeRead, func(w http.ResponseWriter, r *http.Request) {
		answer(w, r, "", "fleet")
	}))
}
