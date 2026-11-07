// SPDX-License-Identifier: AGPL-3.0-or-later

// Package api serves the optional HTTP API used by nepfleet-ui.
//
// /healthz and /api/v1/info are open; everything else needs a bearer token
// (internal/auth) with a fitting scope. Sign-in for people (passkeys, OIDC)
// comes with the milestone "API & security".
package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Serraniel/nepfleet/internal/auth"
	"log"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
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

// HistoryEntry is one commit that touched a stack.
type HistoryEntry struct {
	Commit  string `json:"commit"`
	Date    string `json:"date"` // RFC 3339
	Author  string `json:"author"`
	Subject string `json:"subject"`
	// Applied marks the revision running now.
	Applied bool `json:"applied,omitempty"`
}

// ErrNotFound is what Plan and History return for an unknown stack.
var ErrNotFound = errors.New("no such stack")

// Options configure the API server.
type Options struct {
	Info Info
	// Tokens authenticate requests; nil refuses everything but the open
	// endpoints.
	Tokens *auth.Store
	// TrustedProxies are reverse proxies whose X-Forwarded-For names the
	// client (for the lockout after failed logins).
	TrustedProxies []*net.IPNet
	// Plan is what applying a stack would change (deploy.Plan, secrets
	// masked).
	Plan func(ctx context.Context, stack string) (any, error)
	// History lists the last n commits of a stack, newest first.
	History func(ctx context.Context, stack string, n int) ([]HistoryEntry, error)
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
	mux.HandleFunc("GET /api/v1/stacks", requireToken(o.Tokens, l, o.TrustedProxies, auth.ScopeRead, func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("GET /api/v1/stacks/{name}/plan", requireToken(o.Tokens, l, o.TrustedProxies, auth.ScopeRead, forStack(auth.ScopeRead, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.Plan == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		p, err := o.Plan(r.Context(), name)
		if writeErr(w, err) {
			return
		}
		writeJSON(w, p)
	})))
	mux.HandleFunc("GET /api/v1/stacks/{name}/history", requireToken(o.Tokens, l, o.TrustedProxies, auth.ScopeRead, forStack(auth.ScopeRead, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.History == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		n := 20
		if v := r.URL.Query().Get("n"); v != "" {
			if i, err := strconv.Atoi(v); err == nil && i > 0 && i <= 200 {
				n = i
			}
		}
		h, err := o.History(r.Context(), name, n)
		if writeErr(w, err) {
			return
		}
		if h == nil {
			h = []HistoryEntry{}
		}
		writeJSON(w, h)
	})))
	return secure(cors(o.AllowedOrigins, mux))
}

// stackName mirrors fleet's rule for stack names.
var stackName = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?$`)

// forStack passes on requests about one stack the token covers. Others get
// 404, the same as an unknown stack, so a limited token learns nothing
// about the rest of the fleet.
func forStack(scope string, next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		t, _ := TokenFrom(r.Context())
		if !stackName.MatchString(name) || !t.Allows(scope, name) {
			http.Error(w, ErrNotFound.Error(), http.StatusNotFound)
			return
		}
		next(w, r, name)
	}
}

// writeErr answers an error without internals (paths, git output), which
// go to the server's log. It reports whether there was one.
func writeErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNotFound):
		http.Error(w, ErrNotFound.Error(), http.StatusNotFound)
	default:
		log.Printf("api: %v", err)
		http.Error(w, "the stack cannot be read; see the server log", http.StatusInternalServerError)
	}
	return true
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
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
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
