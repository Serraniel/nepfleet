// SPDX-License-Identifier: AGPL-3.0-or-later

// Package api serves the optional HTTP API used by nepfleet-ui.
//
// /healthz and /api/v1/info are open; everything else needs a bearer token
// (internal/auth) with a fitting scope. Sign-in for people (passkeys, OIDC)
// comes with the milestone "API & security".
package api

import (
	"encoding/json"
	"github.com/Serraniel/nepfleet/internal/auth"
	"net/http"
	"slices"
	"time"
)

// Info is returned by GET /api/v1/info so a frontend can identify a backend.
type Info struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	APIVersion string `json:"apiVersion"`
}

// StackInfo is a stack as GET /api/v1/stacks lists it.
type StackInfo struct {
	Name     string `json:"name"`
	Target   string `json:"target"`
	Mode     string `json:"mode"`
	Apply    string `json:"apply"`
	Template string `json:"template,omitempty"` // source/name@version
	Updates  string `json:"updates"`
}

// Options configure the API server.
type Options struct {
	Info Info
	// Tokens authenticate requests; nil refuses everything but the open
	// endpoints.
	Tokens *auth.Store
	// Stacks lists the fleet's stacks.
	Stacks func() ([]StackInfo, error)
	// AllowedOrigins are the frontends allowed to call this backend from a
	// browser. Empty means same-origin only. "*" is never accepted.
	AllowedOrigins []string
}

// Handler returns the API's http.Handler.
func Handler(o Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/v1/info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, o.Info)
	})
	l := newLimiter()
	mux.HandleFunc("GET /api/v1/stacks", requireToken(o.Tokens, l, auth.ScopeRead, func(w http.ResponseWriter, r *http.Request) {
		if o.Stacks == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		all, err := o.Stacks()
		if err != nil {
			http.Error(w, "the fleet cannot be read", http.StatusInternalServerError)
			return
		}
		t, _ := TokenFrom(r.Context())
		out := []StackInfo{}
		for _, s := range all {
			if t.Allows(auth.ScopeRead, s.Name) {
				out = append(out, s)
			}
		}
		writeJSON(w, out)
	}))
	return secure(cors(o.AllowedOrigins, mux))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Server wraps Handler with conservative timeouts.
func Server(addr string, o Options) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           Handler(o),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func cors(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && origin != "*" && slices.Contains(allowed, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
			h.Set("Access-Control-Max-Age", "600")
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
