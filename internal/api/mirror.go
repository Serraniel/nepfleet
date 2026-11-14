// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// MirrorStatus is how pushing the fleet repository to one of spec.mirrors went.
type MirrorStatus struct {
	// URL without any password or token in it.
	URL   string    `json:"url"`
	OK    time.Time `json:"ok,omitzero"`
	Error string    `json:"error,omitempty"`
}

// RedactURL replaces a URL's user part (a user, password or token) with ***.
func RedactURL(s string) string {
	if u, err := url.Parse(s); err == nil && u.User != nil {
		u.User = url.User("***")
		return u.String()
	}
	return s
}

func registerMirror(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/mirror", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, _ *http.Request) {
		if o.Mirror == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		st, err := o.Mirror(context.Background(), false)
		if writeErr(w, err) {
			return
		}
		writeJSON(w, redactMirrors(st))
	}))
	mux.HandleFunc("POST /api/v1/mirror", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.Mirror == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		st, err := o.Mirror(ctx, true)
		if writeErr(w, err) {
			return
		}
		writeJSON(w, redactMirrors(st))
	}))
}

func redactMirrors(st []MirrorStatus) []MirrorStatus {
	out := make([]MirrorStatus, 0, len(st))
	for _, m := range st {
		out = append(out, MirrorStatus{URL: RedactURL(m.URL), OK: m.OK, Error: redactURLs(m.Error)})
	}
	return out
}
