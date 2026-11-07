// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/remote"
)

// fakeRemote stands in for SSH and the remote's API.
type fakeRemote struct {
	mu       sync.Mutex
	minted   int
	revoked  []string
	mintErr  error
	forwards int
	cut      chan struct{} // closing it drops the connection once
}

func (f *fakeRemote) MintToken(context.Context, remote.Target, string, string) (remote.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mintErr != nil {
		return remote.Token{}, f.mintErr
	}
	f.minted++
	return remote.Token{ID: fmt.Sprintf("%08x", f.minted), Secret: fmt.Sprintf("nft_secret%d", f.minted)}, nil
}

func (f *fakeRemote) RevokeToken(_ context.Context, _ remote.Target, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, id)
	return nil
}

func (f *fakeRemote) Forward(ctx context.Context, _ remote.Target, socket string) error {
	_ = os.Remove(socket)
	l, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s %s auth=%s", r.Method, r.URL.Path, r.Header.Get("Authorization"))
	})}
	go func() { _ = srv.Serve(l) }()
	defer func() { _ = srv.Close() }()
	f.mu.Lock()
	f.forwards++
	cut := f.cut
	f.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-cut:
		return errors.New("connection lost")
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for range 500 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTunnels(t *testing.T) {
	dir, err := os.MkdirTemp("", "ctl") // short: socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f := &fakeRemote{cut: make(chan struct{})}
	s := &Server{Dir: dir, Connector: f, RetryAfter: 10 * time.Millisecond, PollEvery: 10 * time.Millisecond,
		Probe: func(_ context.Context, tg remote.Target) (*remote.Report, error) {
			return &remote.Report{Kernel: "Linux", Target: tg.String()}, nil
		}}
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.stopAll)
	h := s.Handler()
	do := func(method, path, body string, hdr ...string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if len(hdr) == 1 {
			req.Header.Set("Authorization", hdr[0])
		}
		h.ServeHTTP(rec, req)
		return rec
	}
	state := func() Remote {
		var all []Remote
		if err := json.Unmarshal(do(http.MethodGet, "/v1/remotes", "").Body.Bytes(), &all); err != nil || len(all) != 1 {
			t.Fatalf("remotes: %v %v", all, err)
		}
		return all[0]
	}
	if rec := do(http.MethodPost, "/v1/remotes", `{"address":"ssh://nas"}`); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	if got := state(); got.Tunnel != TunnelOff {
		t.Errorf("before connecting: %+v", got)
	}
	if rec := do(http.MethodGet, "/v1/remotes/nas/api/v1/stacks", ""); rec.Code != http.StatusConflict {
		t.Errorf("proxy while off: %d", rec.Code)
	}
	if rec := do(http.MethodPost, "/v1/remotes/nas/connect", ""); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	waitFor(t, "the tunnel", func() bool { return state().Tunnel == TunnelUp })

	// Requests reach /api/... with the daemon's token, not the client's.
	rec := do(http.MethodGet, "/v1/remotes/nas/api/v1/stacks", "", "Bearer client-supplied")
	if want := "GET /api/v1/stacks auth=Bearer nft_secret1"; rec.Body.String() != want {
		t.Errorf("proxied: %q", rec.Body)
	}
	if rec := do(http.MethodGet, "/v1/remotes", ""); strings.Contains(rec.Body.String(), "nft_") {
		t.Errorf("the token must not reach clients: %s", rec.Body)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "remotes.json"))
	if err != nil || strings.Contains(string(raw), "nft_") || !strings.Contains(string(raw), `"connect": true`) {
		t.Errorf("saved state: %s %v", raw, err)
	}

	// A lost connection comes back by itself, with the same fresh token.
	f.mu.Lock()
	close(f.cut)
	f.cut = make(chan struct{})
	f.mu.Unlock()
	waitFor(t, "the reconnect", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.forwards >= 2
	})
	waitFor(t, "the tunnel again", func() bool { return state().Tunnel == TunnelUp })
	f.mu.Lock()
	if f.minted != 1 {
		t.Errorf("a reconnect needs no new token: %d minted", f.minted)
	}
	f.mu.Unlock()

	// A restarted daemon brings it back.
	s.stopAll()
	f.mu.Lock()
	if len(f.revoked) != 1 {
		t.Errorf("stopping must revoke the token: %v", f.revoked)
	}
	f.mu.Unlock()
	s2 := &Server{Dir: dir, Connector: f, RetryAfter: 10 * time.Millisecond, PollEvery: 10 * time.Millisecond}
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	s2.startSaved()
	t.Cleanup(s2.stopAll)
	waitFor(t, "the tunnel after a restart", func() bool {
		s2.mu.Lock()
		defer s2.mu.Unlock()
		tn := s2.tunnels["nas"]
		if tn == nil {
			return false
		}
		st, _ := tn.status()
		return st == TunnelUp
	})
	s2.stopAll()

	// Disconnecting stays off, and removal is clean.
	do(http.MethodPost, "/v1/remotes/nas/connect", "")
	waitFor(t, "the tunnel", func() bool { return state().Tunnel == TunnelUp })
	if rec := do(http.MethodPost, "/v1/remotes/nas/disconnect", ""); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	if got := state(); got.Tunnel != TunnelOff || got.Connect {
		t.Errorf("after disconnect: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "tunnels", "nas.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket left behind: %v", err)
	}

	// A token that can't be made shows why, and it keeps trying.
	f.mu.Lock()
	f.mintErr = errors.New("no nepfleet on the remote")
	f.mu.Unlock()
	do(http.MethodPost, "/v1/remotes/nas/connect", "")
	waitFor(t, "the error", func() bool { return strings.Contains(state().TunnelError, "no nepfleet") })
	if rec := do(http.MethodGet, "/v1/remotes/nas/api/v1/stacks", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("proxy while down: %d", rec.Code)
	}
	if rec := do(http.MethodDelete, "/v1/remotes/nas", ""); rec.Code != http.StatusNoContent {
		t.Errorf("remove: %d", rec.Code)
	}
}

func TestTokenLabel(t *testing.T) {
	for host, want := range map[string]string{
		"HyperNep":              "control-hypernep",
		"my.host_name":          "control-my-host-name",
		"":                      "control",
		"$(x)":                  "control---x",
		strings.Repeat("a", 90): "control-" + strings.Repeat("a", 32),
	} {
		if got := tokenLabel(host); got != want {
			t.Errorf("%q: %q, want %q", host, got, want)
		}
	}
}
