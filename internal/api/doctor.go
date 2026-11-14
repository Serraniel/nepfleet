// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"net/http"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// DoctorCheck is one of nepfleet doctor's results.
type DoctorCheck struct {
	Name string `json:"name"`
	// Status is ok, warn or fail.
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// SecurityFinding is one of nepfleet doctor --security's results.
type SecurityFinding struct {
	Check   string `json:"check"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// DoctorReport is how the server's host stands, as nepfleet doctor and
// nepfleet doctor --security see it.
type DoctorReport struct {
	Host   string        `json:"host"`
	Stacks int           `json:"stacks"`
	Checks []DoctorCheck `json:"checks"`
	// Security is empty with SecurityError set when that check couldn't run.
	Security      []SecurityFinding `json:"security"`
	SecurityError string            `json:"securityError,omitempty"`
}

func registerDoctor(mux *http.ServeMux, o Options, l *limiter) {
	// Admin: the checks name paths, ports and services of the server's host.
	mux.HandleFunc("GET /api/v1/doctor", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.Doctor == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		rep, err := o.Doctor(ctx)
		if writeErr(w, err) {
			return
		}
		if rep.Checks == nil {
			rep.Checks = []DoctorCheck{}
		}
		if rep.Security == nil {
			rep.Security = []SecurityFinding{}
		}
		writeJSON(w, rep)
	}))
}
