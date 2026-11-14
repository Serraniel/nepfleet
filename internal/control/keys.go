// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Serraniel/nepfleet/internal/remote"
)

// Keys unlocks SSH keys with a passphrase into the agent the daemon's ssh uses.
type Keys struct {
	// Askpass is the program ssh-add runs for the passphrase: this binary's
	// `askpass` command. Unlock is refused without it.
	Askpass string
	// Unlock adds a key to the agent; remote.Unlock, replaced in tests.
	Unlock func(r *http.Request, path, passphrase, askpass string) error

	// one unlock at a time, with a pause after a wrong passphrase: guessing
	// through the API is slow, even for a holder of the token.
	mu sync.Mutex
}

// wrongPause is the wait after a wrong passphrase.
var wrongPause = 2 * time.Second

func (s *Server) keyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/keys/unlock", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
			// Passphrase goes to ssh-add for this unlock only: never stored or logged.
			Passphrase string `json:"passphrase"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.Path == "" || req.Passphrase == "" {
			fail(w, http.StatusBadRequest, `send {"path": "<a key in ~/.ssh>", "passphrase": "…"}`)
			return
		}
		if s.Keys.Unlock == nil || s.Keys.Askpass == "" {
			fail(w, http.StatusServiceUnavailable, "this daemon can't unlock keys")
			return
		}
		s.Keys.mu.Lock()
		err := s.Keys.Unlock(r, req.Path, req.Passphrase, s.Keys.Askpass)
		if errors.Is(err, remote.ErrWrongPassphrase) {
			time.Sleep(wrongPause)
		}
		s.Keys.mu.Unlock()
		switch {
		case errors.Is(err, remote.ErrWrongPassphrase):
			fail(w, http.StatusForbidden, err.Error())
		case err != nil:
			fail(w, http.StatusBadRequest, err.Error())
		default:
			log.Printf("control: a key was unlocked for %s", remote.KeyLifetime)
			w.WriteHeader(http.StatusNoContent)
		}
	})
}
