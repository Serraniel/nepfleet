// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
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
	// Action names a fix the app can start: dns-records (POST
	// /api/v1/dns-records), key-backup (POST /api/v1/key/backup).
	Action string `json:"action,omitempty"`
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

// DNSRecordsResult is what turning on Caddy's DNS records committed: the
// Caddy stack, whose plan then shows the change, and the commit.
type DNSRecordsResult struct {
	Stack  string `json:"stack"`
	Commit string `json:"commit"`
}

func registerDNSRecords(mux *http.ServeMux, o Options, l *limiter) {
	// Admin, as the doctor that offers it: it changes the ingress stack.
	mux.HandleFunc("POST /api/v1/dns-records", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.DNSRecords == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Zone string `json:"zone"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.Zone == "" {
			http.Error(w, `send {"zone": "example.org"}`, http.StatusBadRequest)
			return
		}
		t, _ := TokenFrom(r.Context())
		res, err := o.DNSRecords(r.Context(), req.Zone, t.Name)
		var invalid InvalidError
		switch {
		case errors.As(err, &invalid):
			http.Error(w, invalid.Reason, http.StatusUnprocessableEntity)
			return
		case errors.Is(err, ErrUncommitted):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case writeErr(w, err):
			return
		}
		logf("api: %q let Caddy keep DNS records in %s (token %s, commit %s)", t.Name, req.Zone, t.ID, res.Commit)
		writeJSON(w, res)
	}))
}
