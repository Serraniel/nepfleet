// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"

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

// ContainersFunc lists a stack's containers (Options.Containers).
type ContainersFunc func(ctx context.Context, stack string) ([]Container, error)

// ServiceActionFunc acts on one service of a stack (Options.ServiceAction).
type ServiceActionFunc func(ctx context.Context, stack, service, action, by string) error
