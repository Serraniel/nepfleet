// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/remote"
)

// A tunnel down for a locked key tries again as soon as a key is unlocked,
// not after its wait.
func TestUnlockRetriesTunnel(t *testing.T) {
	dir, err := os.MkdirTemp("", "ctl") // short: socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f := &fakeRemote{cut: make(chan struct{}), mintErr: &remote.LockedKeyError{Keys: []string{"/home/me/.ssh/id_rsa"}}}
	s := &Server{Dir: dir, Connector: f, RetryAfter: time.Hour,
		Probe: func(_ context.Context, tg remote.Target) (*remote.Report, error) {
			return &remote.Report{Kernel: "Linux", Target: tg.String(), NepFleetRunning: true}, nil
		}}
	s.Keys = Keys{Askpass: "/usr/bin/nepfleet", Unlock: func(*http.Request, string, string, string) error { return nil }}
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.stopAll)
	h := s.Handler()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	if rec := do(http.MethodPost, "/v1/remotes", `{"address":"ssh://nas"}`); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	if rec := do(http.MethodPost, "/v1/remotes/nas/connect", ""); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	tunnelState := func() string {
		s.mu.Lock()
		tn := s.tunnels["nas"]
		s.mu.Unlock()
		state, _ := tn.status()
		return state
	}
	waitFor(t, "the locked key", func() bool {
		return strings.Contains(do(http.MethodGet, "/v1/remotes", "").Body.String(), "lockedKeys")
	})
	f.mu.Lock()
	f.mintErr = nil
	f.mu.Unlock()
	if rec := do(http.MethodPost, "/v1/keys/unlock", `{"path":"/home/me/.ssh/id_rsa","passphrase":"right"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("unlock: %d %s", rec.Code, rec.Body)
	}
	waitFor(t, "the tunnel", func() bool { return tunnelState() == TunnelUp })
}
