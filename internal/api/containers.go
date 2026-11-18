// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// Container is one container (Compose) or task (Swarm) of a stack.
type Container struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	// State is running, exited, restarting, … (Compose), or the task's
	// current state (Swarm).
	State string `json:"state"`
	// Health is healthy, unhealthy or starting, or empty without a health check.
	Health   string `json:"health,omitempty"`
	Status   string `json:"status,omitempty"` // Docker's words, e.g. "Up 2 hours"
	Image    string `json:"image"`
	Restarts int    `json:"restarts"`
	Node     string `json:"node,omitempty"` // Swarm: where the task runs
	Error    string `json:"error,omitempty"`
}

// Service actions over the API.
const (
	ServiceRestart = "restart"
	ServiceStop    = "stop"
	ServiceStart   = "start"
)

// ErrNotSupported is an action the stack's mode doesn't offer.
var ErrNotSupported = errors.New("not supported for this stack")

var serviceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

func registerContainers(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/stacks/{name}/containers", requireAuth(o, l, auth.ScopeRead, forStack(auth.ScopeRead, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.Containers == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		cs, err := o.Containers(r.Context(), name)
		if writeErr(w, err) {
			return
		}
		if cs == nil {
			cs = []Container{}
		}
		writeJSON(w, cs)
	})))
	mux.HandleFunc("POST /api/v1/stacks/{name}/services/{service}/{action}", requireAuth(o, l, auth.ScopeApply, forStack(auth.ScopeApply, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.ServiceAction == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		service, action := r.PathValue("service"), r.PathValue("action")
		if !serviceName.MatchString(service) {
			http.Error(w, ErrNotFound.Error(), http.StatusNotFound)
			return
		}
		switch action {
		case ServiceRestart, ServiceStop, ServiceStart:
		default:
			http.Error(w, "the action is restart, stop or start", http.StatusNotFound)
			return
		}
		t, _ := TokenFrom(r.Context())
		err := o.ServiceAction(r.Context(), name, service, action, t.Name)
		if errors.Is(err, ErrNotSupported) {
			http.Error(w, action+" is "+err.Error()+" (Swarm services restart only)", http.StatusUnprocessableEntity)
			return
		}
		if writeErr(w, err) {
			return
		}
		logf("api: %q: %s %s/%s (token %s)", t.Name, action, name, service, t.ID)
		w.WriteHeader(http.StatusNoContent)
	})))
}

// MaxLogLines bounds the lines one logs request returns.
const MaxLogLines = 2000

// LogsFunc returns the last lines of one container's (Compose) or service's
// (Swarm) output, stdout and stderr, with timestamps (Options.Logs).
type LogsFunc func(ctx context.Context, stack, container string, lines int) (string, error)

func registerLogs(mux *http.ServeMux, o Options, l *limiter) {
	// Edit scope: logs often hold what reading secrets would show.
	mux.HandleFunc("GET /api/v1/stacks/{name}/containers/{container}/logs", requireAuth(o, l, auth.ScopeEdit, forStack(auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request, name string) {
		container := r.PathValue("container")
		if !serviceName.MatchString(container) {
			http.Error(w, ErrNotFound.Error(), http.StatusNotFound)
			return
		}
		lines := 200
		if v := r.URL.Query().Get("lines"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > MaxLogLines {
				http.Error(w, "lines is 1 to "+strconv.Itoa(MaxLogLines), http.StatusBadRequest)
				return
			}
			lines = n
		}
		if r.URL.Query().Get("follow") == "1" {
			followLogs(w, r, o, name, container, lines)
			return
		}
		if o.Logs == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		out, err := o.Logs(r.Context(), name, container, lines)
		if writeErr(w, err) {
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(out))
	})))
}

// FollowLogsFunc writes a container's last lines and then its new output to
// out as it comes, until ctx ends (Options.FollowLogs). An unknown container
// is ErrNotFound, returned before anything is written.
type FollowLogsFunc func(ctx context.Context, stack, container string, lines int, out io.Writer) error

// Following logs: at most so many at once, each for so long (a token can be
// revoked meanwhile; the app starts a new one).
var (
	maxLogFollows     int64 = 16
	logFollowLifetime       = 30 * time.Minute
)

var logFollows atomic.Int64

// streamWriter sends each write at once, with its own write deadline: the
// server's limit is for short answers. The status line goes with the first.
type streamWriter struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	started bool
}

func (s *streamWriter) Write(p []byte) (int, error) {
	if !s.started {
		h := s.w.Header()
		h.Set("Content-Type", "text/plain; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Accel-Buffering", "no")
		s.w.WriteHeader(http.StatusOK)
		s.started = true
	}
	if err := s.rc.SetWriteDeadline(time.Now().Add(streamWriteWindow)); err != nil {
		return 0, err
	}
	n, err := s.w.Write(p)
	if err != nil {
		return n, err
	}
	return n, s.rc.Flush()
}

func followLogs(w http.ResponseWriter, r *http.Request, o Options, stack, container string, lines int) {
	if o.FollowLogs == nil {
		http.Error(w, "following logs isn't supported here", http.StatusNotImplemented)
		return
	}
	if logFollows.Add(1) > maxLogFollows {
		logFollows.Add(-1)
		w.Header().Set("Retry-After", "30")
		http.Error(w, "too many logs followed at once", http.StatusServiceUnavailable)
		return
	}
	defer logFollows.Add(-1)
	ctx, cancel := context.WithTimeout(r.Context(), logFollowLifetime)
	defer cancel()
	sw := &streamWriter{w: w, rc: http.NewResponseController(w)}
	err := o.FollowLogs(ctx, stack, container, lines, sw)
	if !sw.started {
		if err == nil {
			sw.started = true
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			return
		}
		writeErr(w, err)
		return
	}
	if err != nil && ctx.Err() == nil {
		logf("api: following %s/%s: %v", stack, container, err)
	}
}

// ContainersFunc lists a stack's containers (Options.Containers).
type ContainersFunc func(ctx context.Context, stack string) ([]Container, error)

// ServiceActionFunc acts on one service of a stack (Options.ServiceAction).
type ServiceActionFunc func(ctx context.Context, stack, service, action, by string) error
