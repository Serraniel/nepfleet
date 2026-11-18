// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// NotifyRoute is one of fleet.yaml's notifications.routes: which events go
// to which channels.
type NotifyRoute struct {
	Channels []string `json:"channels"`
	// Events are names or patterns ("apply.*"); empty matches all.
	Events []string `json:"events"`
	// Stacks limits the route to these stacks; empty matches all.
	Stacks      []string `json:"stacks"`
	MinSeverity string   `json:"minSeverity,omitempty"`
}

// RoutesFunc lists the fleet's routes (Options.Routes).
type RoutesFunc func() ([]NotifyRoute, error)

// SetRoutesFunc replaces the fleet's routes and commits fleet.yaml as by;
// it returns the commit (Options.SetRoutes).
type SetRoutesFunc func(ctx context.Context, routes []NotifyRoute, by string) (string, error)

// eventPattern is an event name or a pattern of one, as routes take them.
var eventPattern = regexp.MustCompile(`^(\*|[a-z][a-z0-9_.-]{0,63}\*?)$`)

const maxRoutes = 100

func registerRoutes(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/notifications/routes", requireAuth(o, l, auth.ScopeRead, func(w http.ResponseWriter, _ *http.Request) {
		if o.Routes == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		rs, err := o.Routes()
		if writeErr(w, err) {
			return
		}
		if rs == nil {
			rs = []NotifyRoute{}
		}
		writeJSON(w, map[string]any{"routes": rs})
	}))
	mux.HandleFunc("PUT /api/v1/notifications/routes", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.SetRoutes == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Routes []NotifyRoute `json:"routes"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.Routes == nil || len(req.Routes) > maxRoutes {
			http.Error(w, `send {"routes": [{"channels": ["…"], "events": ["apply.*"], "stacks": [], "minSeverity": "warning"}]}`, http.StatusBadRequest)
			return
		}
		for _, rt := range req.Routes {
			if reason := checkRoute(rt); reason != "" {
				http.Error(w, reason, http.StatusBadRequest)
				return
			}
		}
		t, _ := TokenFrom(r.Context())
		commit, err := o.SetRoutes(r.Context(), req.Routes, t.Name)
		if channelErr(w, err) {
			return
		}
		logf("api: %s set %d notification routes (token %s)", t.Name, len(req.Routes), t.ID)
		writeJSON(w, map[string]string{"commit": commit})
	}))
}

// checkRoute says what's wrong with a route before fleet.yaml is touched;
// unknown channels are refused when the fleet loads it.
func checkRoute(rt NotifyRoute) string {
	if len(rt.Channels) == 0 {
		return "every route needs at least one channel"
	}
	for _, c := range rt.Channels {
		if !channelName.MatchString(c) {
			return "a channel name is 1 to 64 letters, digits, dots, dashes or underscores"
		}
	}
	for _, e := range rt.Events {
		if !eventPattern.MatchString(e) {
			return "an event is a name such as apply.failed, a pattern such as apply.*, or *"
		}
	}
	for _, s := range rt.Stacks {
		if !stackName.MatchString(s) {
			return s + " is not a stack name"
		}
	}
	if rt.MinSeverity != "" && !slices.Contains([]string{"info", "warning", "error"}, rt.MinSeverity) {
		return "minSeverity is info, warning or error"
	}
	if len(rt.Channels) > 50 || len(rt.Events) > 50 || len(rt.Stacks) > 200 {
		return "a route lists too many channels, events or stacks"
	}
	return ""
}
