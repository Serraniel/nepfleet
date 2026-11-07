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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Serraniel/nepfleet/internal/events"
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
	events   []events.Event
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
	network := "tcp"
	if !strings.HasPrefix(socket, "127.0.0.1:") {
		network = "unix"
		_ = os.Remove(socket)
	}
	l, err := net.Listen(network, socket)
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/events" {
			after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
			f.mu.Lock()
			defer f.mu.Unlock()
			out := []events.Event{}
			for _, e := range f.events {
				if e.ID > after {
					out = append(out, e)
				}
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
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
	if rec := do(http.MethodGet, "/v1/remotes/nas/api/%2e%2e/%2e%2e/healthz", ""); rec.Code == http.StatusOK {
		t.Errorf("an encoded .. must not leave /api/: %s", rec.Body)
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
		"MyHost":              "control-myhost",
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

func TestFeed(t *testing.T) {
	dir, err := os.MkdirTemp("", "ctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	f := &fakeRemote{cut: make(chan struct{}), events: []events.Event{{ID: 1, Name: "old", Title: "history"}, {ID: 2, Name: "old", Title: "history"}}}
	mk := func() *Server {
		s := &Server{Dir: dir, Connector: f, RetryAfter: 10 * time.Millisecond, PollEvery: 10 * time.Millisecond,
			Probe: func(_ context.Context, tg remote.Target) (*remote.Report, error) {
				return &remote.Report{Kernel: "Linux", Target: tg.String()}, nil
			}}
		if err := s.Load(); err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := mk()
	t.Cleanup(s.stopAll)
	type list struct {
		Events []events.Event `json:"events"`
		Unread int            `json:"unread"`
		Seen   int64          `json:"seen"`
	}
	get := func(s *Server) list {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/events", nil))
		var l list
		if err := json.Unmarshal(rec.Body.Bytes(), &l); err != nil {
			t.Fatal(rec.Body, err)
		}
		return l
	}
	post := func(s *Server, method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	if rec := post(s, http.MethodPost, "/v1/remotes", `{"address":"ssh://nas"}`); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	post(s, http.MethodPost, "/v1/remotes/nas/connect", "")
	feedFile := filepath.Join(dir, "events", "state.json")
	waitFor(t, "the first poll", func() bool { _, err := os.Stat(feedFile); return err == nil })
	if l := get(s); len(l.Events) != 0 || l.Unread != 0 {
		t.Errorf("history is not news: %+v", l)
	}
	add := func(e events.Event) {
		f.mu.Lock()
		f.events = append(f.events, e)
		f.mu.Unlock()
	}
	add(events.Event{ID: 3, Name: "apply.failed", Severity: "error", Stack: "web", Title: "failed", Message: strings.Repeat("é", maxEventMessage)})
	waitFor(t, "an event", func() bool { return len(get(s).Events) == 1 })
	l := get(s)
	if e := l.Events[0]; e.Fleet != "nas" || e.Name != "apply.failed" || l.Unread != 1 || e.ID != 1 {
		t.Errorf("merged: %+v", l)
	}
	if m := l.Events[0].Message; len(m) > maxEventMessage+4 || !utf8.ValidString(m) {
		t.Errorf("message: %d bytes, valid %v", len(m), utf8.ValidString(m))
	}
	// Seen goes forward only.
	if rec := post(s, http.MethodPost, "/v1/events/seen", `{"id":1}`); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Body)
	}
	post(s, http.MethodPost, "/v1/events/seen", `{"id":0}`)
	if l := get(s); l.Unread != 0 || l.Seen != 1 {
		t.Errorf("seen: %+v", l)
	}
	if rec := post(s, http.MethodPost, "/v1/events/seen", `nonsense`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad body: %d", rec.Code)
	}

	// What happens while the daemon is down arrives after its restart, once.
	s.stopAll()
	add(events.Event{ID: 4, Name: "container.oom", Title: "oom"})
	s2 := mk()
	s2.startSaved()
	t.Cleanup(s2.stopAll)
	waitFor(t, "catching up", func() bool { return len(get(s2).Events) == 2 })
	time.Sleep(100 * time.Millisecond)
	if l := get(s2); len(l.Events) != 2 || l.Unread != 1 || l.Events[1].Name != "container.oom" {
		t.Errorf("after a restart: %+v", l)
	}
	// A remote can't replay old IDs into the feed.
	add(events.Event{ID: 2, Name: "replay", Title: "x"})
	time.Sleep(100 * time.Millisecond)
	if l := get(s2); len(l.Events) != 2 {
		t.Errorf("a replay was stored: %+v", l)
	}
}
