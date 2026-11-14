// SPDX-License-Identifier: AGPL-3.0-or-later

// Package api serves the optional HTTP API used by nepfleet-ui.
//
// /healthz, /api/v1/info and /api/v1/openapi.yaml are open; everything else needs
// a bearer token (internal/auth) with a fitting scope, or a person's session.
// openapi.yaml describes it all.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Serraniel/nepfleet/internal/auth"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
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
	// URL is where its web UI is, if its settings say.
	URL string `json:"url,omitempty"`
	// Links are other places to look at it, such as its logs.
	Links []StackLink `json:"links,omitempty"`
	// Uses are the values it takes from other stacks: names only, never values.
	Uses []StackRef `json:"uses,omitempty"`
	// State is running, unhealthy, partial or stopped, from this host's containers;
	// empty for stacks on other hosts, swarm stacks, and ones not applied.
	State string `json:"state,omitempty"`
}

// StackRef is one value a stack takes from another, as {from, value|secret}.
type StackRef struct {
	// Key is the name of the value in this stack.
	Key  string `json:"key"`
	From string `json:"from"`
	// Value or Secret names what's taken.
	Value  string `json:"value,omitempty"`
	Secret string `json:"secret,omitempty"`
}

// StackLink is a place to look at a stack.
type StackLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
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
	// Accounts lets people sign in with passkeys; nil means tokens only.
	Accounts *Accounts
	// TrustedProxies are reverse proxies whose X-Forwarded-For names the
	// client (for the lockout after failed logins).
	TrustedProxies []*net.IPNet
	// Plan is what applying a stack would change (deploy.Plan, secrets
	// masked).
	Plan func(ctx context.Context, stack string, updateLock bool) (any, error)
	// StartApply checks that the stack's plan still has the ID the client
	// reviewed (and that destructive changes are confirmed) and returns the
	// apply to run in the background. Errors: ErrNotFound, ErrBusy,
	// ErrPlanChanged, ErrNeedsConfirm.
	StartApply func(ctx context.Context, stack, planID string, confirmDestructive, updateLock bool, by string) (func(log io.Writer) error, error)
	// StartRollback returns a rollback to a commit of the stack's own history,
	// to run in the background.
	StartRollback func(ctx context.Context, stack, commit string, confirmDestructive bool, by string) (func(log io.Writer) error, error)
	// Templates is the catalogue of every template source of the fleet.
	Templates func(ctx context.Context) (any, error)
	// Events returns events newer than the ID, at most limit, as far as allow
	// lets through.
	Events func(after int64, limit int, allow func(stack string) bool) (any, error)
	// History lists the last n commits of a stack, newest first.
	History func(ctx context.Context, stack string, n int) ([]HistoryEntry, error)
	// Stacks lists the fleet's stacks.
	Stacks func() ([]StackInfo, error)
	// Updates is what the registries have for a stack's images: newer tags in
	// and beyond each track, and tags rebuilt since lock.yaml pinned them.
	Updates func(ctx context.Context, stack string) ([]ImageUpdate, error)
	// Maintenance lists the maintenance windows of the fleet's Uptime Kuma
	// instances.
	Maintenance func(ctx context.Context) ([]KumaMaintenance, error)
	// Egress is what a stack's egress sidecars recorded or refused.
	Egress func(ctx context.Context, stack string) (*StackEgress, error)
	// SyncStatus is where the fleet repository stands against its origin;
	// SyncNow starts a sync in the background (nil: sync is off). SyncSecret
	// verifies forges' webhook signatures (empty: webhooks are refused).
	SyncStatus func() SyncStatus
	SyncNow    func()
	SyncSecret string
	// NewStack creates and commits a stack from a template, without applying
	// it. Errors: ErrStackExists, InvalidError, ErrBusy.
	NewStack func(ctx context.Context, req NewStack, by string) (NewStackResult, error)
	// Channels names fleet.yaml's notification channels; TestChannel sends
	// one a test message (ErrNoChannel for an unknown name).
	Channels    func() ([]string, error)
	TestChannel func(ctx context.Context, name string) error
	// Features are the opt-in system stacks and the stacks providing them.
	Features func() ([]Feature, error)
	// SecretKeys lists the keys of a stack's encrypted values, or of the
	// fleet's with stack "". SetSecret stores one encrypted and commits it as
	// by; a nil value removes it (ErrNoSecret when there is none). Values are
	// never read back.
	SecretKeys func(ctx context.Context, stack string) ([]string, error)
	SetSecret  func(ctx context.Context, stack, key string, value *string, by string) (commit string, err error)
	// StackFiles lists a stack's editable files, without encrypted values (edit scope:
	// files/ may hold plain configuration a read token shouldn't see).
	StackFiles func(ctx context.Context, stack string) ([]StackFile, error)
	// EditStack writes and commits a change to a stack's files, without
	// applying it. Errors: ErrStale, ErrBadPath, InvalidError, ErrBusy.
	EditStack func(ctx context.Context, stack string, e StackEdit, by string, stacks []string) (EditResult, error)
	// Alert sends an alert from another tool to the fleet's notification
	// channels, with their dedup and quiet hours.
	Alert func(ctx context.Context, a Alert) error
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
	mux.HandleFunc("GET /api/v1/openapi.yaml", serveOpenAPI)
	l := newLimiter()
	mux.HandleFunc("GET /api/v1/stacks", requireAuth(o, l, auth.ScopeRead, func(w http.ResponseWriter, r *http.Request) {
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
				// A stack this token doesn't cover stays unnamed here too.
				s.Uses = slices.DeleteFunc(slices.Clone(s.Uses), func(u StackRef) bool {
					return !t.Allows(auth.ScopeRead, u.From)
				})
				out = append(out, s)
			}
		}
		writeJSON(w, out)
	}))
	mux.HandleFunc("GET /api/v1/stacks/{name}/plan", requireAuth(o, l, auth.ScopeRead, forStack(auth.ScopeRead, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.Plan == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		p, err := o.Plan(r.Context(), name, r.URL.Query().Get("updateLock") == "true")
		if writeErr(w, err) {
			return
		}
		writeJSON(w, p)
	})))
	mux.HandleFunc("GET /api/v1/templates", requireAuth(o, l, auth.ScopeRead, func(w http.ResponseWriter, r *http.Request) {
		if o.Templates == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		c, err := o.Templates(r.Context())
		if writeErr(w, err) {
			return
		}
		writeJSON(w, c)
	}))
	mux.HandleFunc("GET /metrics", requireAuth(o, l, auth.ScopeRead, serveMetrics(o)))
	mux.HandleFunc("GET /api/v1/events/stream", requireAuth(o, l, auth.ScopeRead, streamEvents(o)))
	mux.HandleFunc("GET /api/v1/events", requireAuth(o, l, auth.ScopeRead, func(w http.ResponseWriter, r *http.Request) {
		if o.Events == nil {
			http.Error(w, "no events", http.StatusServiceUnavailable)
			return
		}
		q := r.URL.Query()
		after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
		limit := 100
		if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 500 {
			limit = n
		}
		t, _ := TokenFrom(r.Context())
		// Events about the whole host are for tokens that cover everything.
		allow := func(stack string) bool {
			if stack == "" {
				return len(t.Stacks) == 0
			}
			return t.Allows(auth.ScopeRead, stack)
		}
		ev, err := o.Events(after, limit, allow)
		if writeErr(w, err) {
			return
		}
		writeJSON(w, ev)
	}))
	js := newJobs()
	mux.HandleFunc("POST /api/v1/stacks/{name}/apply", requireAuth(o, l, auth.ScopeApply, forStack(auth.ScopeApply, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.StartApply == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Plan               string `json:"plan"`
			ConfirmDestructive bool   `json:"confirmDestructive"`
			// UpdateLock applies a plan made with ?updateLock=true: newer digests
			// and the template source's current commit.
			UpdateLock bool `json:"updateLock"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.Plan == "" {
			http.Error(w, `send {"plan": "<id of the reviewed plan>"}`, http.StatusBadRequest)
			return
		}
		t, _ := TokenFrom(r.Context())
		run, err := o.StartApply(r.Context(), name, req.Plan, req.ConfirmDestructive, req.UpdateLock, t.Name)
		switch {
		case errors.Is(err, ErrBusy), errors.Is(err, ErrPlanChanged), errors.Is(err, ErrNeedsConfirm):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case writeErr(w, err):
			return
		}
		log.Printf("api: %s applies %s (token %s)", t.Name, name, t.ID)
		job := js.start("apply", name, t.Name, func(l *jobLog) error { return run(l) })
		w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, job)
	})))
	mux.HandleFunc("POST /api/v1/stacks/{name}/rollback", requireAuth(o, l, auth.ScopeApply, forStack(auth.ScopeApply, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.StartRollback == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Commit             string `json:"commit"`
			ConfirmDestructive bool   `json:"confirmDestructive"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || !fullCommit.MatchString(req.Commit) {
			http.Error(w, `send {"commit": "<full commit ID from the history>"}`, http.StatusBadRequest)
			return
		}
		t, _ := TokenFrom(r.Context())
		run, err := o.StartRollback(r.Context(), name, req.Commit, req.ConfirmDestructive, t.Name)
		switch {
		case errors.Is(err, ErrBusy), errors.Is(err, ErrNotInHistory):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case writeErr(w, err):
			return
		}
		log.Printf("api: %s rolls %s back to %s (token %s)", t.Name, name, req.Commit, t.ID)
		job := js.start("rollback", name, t.Name, func(l *jobLog) error { return run(l) })
		w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, job)
	})))
	registerAlerts(mux, o, l)
	registerUpdates(mux, o, l)
	registerMaintenance(mux, o, l)
	registerEdit(mux, o, l)
	registerSync(mux, o, l)
	registerNewStack(mux, o, l)
	registerSecrets(mux, o, l)
	registerFeatures(mux, o, l)
	registerChannels(mux, o, l)
	registerEgress(mux, o, l)
	mux.HandleFunc("GET /api/v1/jobs/{id}", requireAuth(o, l, auth.ScopeRead, func(w http.ResponseWriter, r *http.Request) {
		job, ok := js.get(r.PathValue("id"))
		t, _ := TokenFrom(r.Context())
		if !ok || !t.Allows(auth.ScopeRead, job.Stack) {
			http.Error(w, "no such job", http.StatusNotFound)
			return
		}
		writeJSON(w, job)
	}))
	mux.HandleFunc("GET /api/v1/stacks/{name}/history", requireAuth(o, l, auth.ScopeRead, forStack(auth.ScopeRead, func(w http.ResponseWriter, r *http.Request, name string) {
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
	if o.Accounts != nil {
		o.Accounts.routes(mux, l, o, o.sessionOrigins())
	}
	return secure(cors(o.AllowedOrigins, mux))
}

// fullCommit is a whole commit ID: never a ref or an option for git.
var fullCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

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
	case errors.Is(err, ErrBusy):
		w.Header().Set("Retry-After", "10")
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		log.Printf("api: %v", err)
		http.Error(w, "the stack cannot be read; see the server log", http.StatusInternalServerError)
	}
	return true
}

// CheckOrigin accepts an exact origin (scheme, host, optional port), which is
// all a browser sends. "*" and anything with a path would silently match
// nothing, or too much.
func CheckOrigin(o string) error {
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%q is not an exact origin such as https://ui.example.org", o)
	}
	return nil
}

// sessionOrigins may use a session: the allowed origins and the passkey origins.
func (o Options) sessionOrigins() []string {
	out := slices.Clone(o.AllowedOrigins)
	if o.Accounts != nil && o.Accounts.WebAuthn != nil {
		out = append(out, o.Accounts.WebAuthn.Config.RPOrigins...)
	}
	return out
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
