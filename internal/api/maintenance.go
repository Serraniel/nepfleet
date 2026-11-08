// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// KumaMaintenance is one Uptime Kuma instance of the fleet and its maintenance
// windows, or why they couldn't be read.
type KumaMaintenance struct {
	Kuma    string              `json:"kuma"`
	Error   string              `json:"error,omitempty"`
	Windows []MaintenanceWindow `json:"windows"`
}

// MaintenanceWindow is a window as Kuma lists it.
type MaintenanceWindow struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Strategy    string `json:"strategy"`
	Active      bool   `json:"active"`
	// Status is under-maintenance, scheduled, ended, inactive or unknown.
	Status string `json:"status"`
	// Start and End are its next or current slot, as Kuma reports it.
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

func registerMaintenance(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/maintenance", requireAuth(o, l, auth.ScopeRead, func(w http.ResponseWriter, r *http.Request) {
		if o.Maintenance == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		// Titles name stacks: fleet-wide, like host events.
		if t, _ := TokenFrom(r.Context()); len(t.Stacks) > 0 {
			http.Error(w, "a token limited to stacks can't see fleet-wide maintenance", http.StatusForbidden)
			return
		}
		m, err := o.Maintenance(r.Context())
		if writeErr(w, err) {
			return
		}
		if m == nil {
			m = []KumaMaintenance{}
		}
		writeJSON(w, m)
	}))
}
