// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// Backups are the archives a stack can be restored from: the volume-backup
// archives of its host, newest first, and the stack's volume keys.
type Backups struct {
	Folder   string   `json:"folder"`
	Archives []string `json:"archives"`
	Volumes  []string `json:"volumes"`
}

// RestoreRequest restores a stack's volumes from an archive, as nepfleet
// restore. Confirm repeats the stack's name: its containers stop and its
// data is replaced (a safety copy of each volume is kept).
type RestoreRequest struct {
	Archive string   `json:"archive"`
	Volumes []string `json:"volumes,omitempty"`
	Confirm string   `json:"confirm"`
}

var volumeKey = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func registerRestore(mux *http.ServeMux, o Options, l *limiter, js *jobs) {
	mux.HandleFunc("GET /api/v1/stacks/{name}/backups", requireAuth(o, l, auth.ScopeEdit, forStack(auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.Backups == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		b, err := o.Backups(ctx, name)
		var invalid InvalidError
		if errors.As(err, &invalid) {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if writeErr(w, err) {
			return
		}
		writeJSON(w, b)
	})))
	mux.HandleFunc("POST /api/v1/stacks/{name}/restore", requireAuth(o, l, auth.ScopeEdit, forStack(auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.StartRestore == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		if !mayChangeAccess(w, r, o) {
			return
		}
		var req RestoreRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.Archive == "" || len(req.Volumes) > 50 {
			http.Error(w, `send {"archive": "backup-….tar.gz.age", "volumes": ["db"], "confirm": "<stack name>"}`, http.StatusBadRequest)
			return
		}
		if req.Confirm != name {
			http.Error(w, "type the stack's name to confirm: it stops while its data is replaced", http.StatusConflict)
			return
		}
		for _, v := range req.Volumes {
			if !volumeKey.MatchString(v) {
				http.Error(w, v+" is not a volume key", http.StatusBadRequest)
				return
			}
		}
		t, _ := TokenFrom(r.Context())
		run, err := o.StartRestore(r.Context(), name, req.Archive, req.Volumes, t.Name)
		var invalid InvalidError
		switch {
		case errors.As(err, &invalid):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		case errors.Is(err, ErrBusy):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case writeErr(w, err):
			return
		}
		log.Printf("api: %q restores %s from %s (token %s)", t.Name, name, req.Archive, t.ID) //nolint:gosec // %q escapes the name; checked names
		job := js.start("restore", name, t.Name, func(l *jobLog) error { return run(l) })
		w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, job)
	})))
}
