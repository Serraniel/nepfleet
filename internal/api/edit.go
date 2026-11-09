// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// StackFile is one of a stack's files, for the editor. Encrypted values are
// never one of them.
type StackFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	// Revision is the file's git blob hash, sent back as base when changing it.
	Revision string `json:"revision"`
}

// StackEdit changes some of a stack's files in one commit.
type StackEdit struct {
	// Files are paths in the stack's directory and their new content.
	Files map[string]string `json:"files"`
	// Base is each changed file's revision as the client read it; empty for a
	// new file.
	Base map[string]string `json:"base"`
	// Message is the commit's subject.
	Message string `json:"message"`
}

// EditResult is the commit an edit made.
type EditResult struct {
	Commit string `json:"commit"`
}

var (
	// ErrStale means a file changed since the client read it.
	ErrStale = errors.New("a file changed since you opened it; reload it and edit again")
	// ErrBadPath means a path outside the stack, or one the editor may not touch.
	ErrBadPath = errors.New("that path isn't one of the stack's editable files")
)

// maxEditFiles caps the files one edit may change.
const maxEditFiles = 100

// InvalidError is a change refused because the stack would no longer be valid.
// Reason has server paths removed, and is safe to show.
type InvalidError struct{ Reason string }

func (e InvalidError) Error() string { return "not saved: " + e.Reason }

// recentlyVerified reports whether a person's session proved itself lately, as
// changing what runs needs. Tokens have no session: their scope decides.
func (a *Accounts) recentlyVerified(r *http.Request) bool {
	if a == nil {
		return false
	}
	c, err := r.Cookie(SessionCookie)
	return err == nil && a.Sessions.RecentlyVerified(c.Value)
}

func registerEdit(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/stacks/{name}/files", requireAuth(o, l, auth.ScopeEdit, forStack(auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.StackFiles == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		files, err := o.StackFiles(r.Context(), name)
		if writeErr(w, err) {
			return
		}
		if files == nil {
			files = []StackFile{}
		}
		writeJSON(w, files)
	})))
	mux.HandleFunc("PUT /api/v1/stacks/{name}/files", requireAuth(o, l, auth.ScopeEdit, forStack(auth.ScopeEdit, func(w http.ResponseWriter, r *http.Request, name string) {
		if o.EditStack == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		t, _ := TokenFrom(r.Context())
		if t.ID == "session" && !o.Accounts.recentlyVerified(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"verify":true,"message":"confirm it's you with a passkey, then save again"}` + "\n"))
			return
		}
		var e StackEdit
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil || len(e.Files) == 0 {
			http.Error(w, `send {"files": {"path": "content"}, "base": {"path": "revision"}, "message": "…"}`, http.StatusBadRequest)
			return
		}
		if len(e.Files) > maxEditFiles {
			http.Error(w, fmt.Sprintf("at most %d files in one edit", maxEditFiles), http.StatusBadRequest)
			return
		}
		// A token limited to stacks may not reach others through references.
		res, err := o.EditStack(r.Context(), name, e, t.Name, t.Stacks)
		var invalid InvalidError
		switch {
		case errors.Is(err, ErrStale):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case errors.Is(err, ErrBadPath):
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		case errors.As(err, &invalid):
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		case errors.Is(err, ErrBusy):
			http.Error(w, err.Error(), http.StatusConflict)
			return
		case writeErr(w, err):
			return
		}
		log.Printf("api: %s changed %d file(s) of %s in %s (token %s)", t.Name, len(e.Files), name, res.Commit, t.ID)
		writeJSON(w, res)
	})))
}
