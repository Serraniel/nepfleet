// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

type streamFixture struct {
	mu  sync.Mutex
	evs []streamTestEvent
}

type streamTestEvent struct {
	ID    int64  `json:"id"`
	Stack string `json:"stack,omitempty"`
	Title string `json:"title"`
}

func (f *streamFixture) add(e streamTestEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evs = append(f.evs, e)
}

func (f *streamFixture) after(after int64, limit int, allow func(string) bool) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []streamTestEvent{}
	for _, e := range f.evs {
		if e.ID > after && allow(e.Stack) && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

func fastStream(t *testing.T) {
	t.Helper()
	poll, beat, life := streamPoll, streamHeartbeat, streamLifetime
	streamPoll, streamHeartbeat, streamLifetime = 5*time.Millisecond, 20*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() { streamPoll, streamHeartbeat, streamLifetime = poll, beat, life })
}

func streamServer(t *testing.T, f *streamFixture, stacks ...string) (*httptest.Server, string) {
	t.Helper()
	tokens := &auth.Store{Dir: t.TempDir()}
	secret, _, err := tokens.Create("reader", auth.ScopeRead, stacks, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Options{Tokens: tokens, Events: f.after}))
	t.Cleanup(srv.Close)
	return srv, secret
}

func readStream(t *testing.T, url, secret string, header map[string]string) []string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var lines []string
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

func TestStreamSendsNewEventsLive(t *testing.T) {
	fastStream(t)
	f := &streamFixture{}
	f.add(streamTestEvent{ID: 1, Title: "old"})
	f.add(streamTestEvent{ID: 2, Title: "seen"})
	srv, secret := streamServer(t, f)
	go func() {
		time.Sleep(50 * time.Millisecond)
		f.add(streamTestEvent{ID: 3, Title: "line\nbreak"})
	}()
	lines := strings.Join(readStream(t, srv.URL+"/api/v1/events/stream?after=1", secret, nil), "\n")
	if strings.Contains(lines, `"old"`) {
		t.Errorf("an event from before: %s", lines)
	}
	for _, want := range []string{"id: 2\nevent: event\ndata: {", `"seen"`, "id: 3\n", `line\nbreak`, ": connected", ": still here"} {
		if !strings.Contains(lines, want) {
			t.Errorf("no %q in:\n%s", want, lines)
		}
	}
}

func TestStreamResumesFromLastEventID(t *testing.T) {
	fastStream(t)
	f := &streamFixture{}
	f.add(streamTestEvent{ID: 5, Title: "five"})
	f.add(streamTestEvent{ID: 6, Title: "six"})
	srv, secret := streamServer(t, f)
	lines := strings.Join(readStream(t, srv.URL+"/api/v1/events/stream", secret, map[string]string{"Last-Event-ID": "5"}), "\n")
	if strings.Contains(lines, `"five"`) || !strings.Contains(lines, `"six"`) {
		t.Errorf("%s", lines)
	}
}

func TestStreamFollowsTheToken(t *testing.T) {
	fastStream(t)
	f := &streamFixture{}
	f.add(streamTestEvent{ID: 1, Stack: "web", Title: "mine"})
	f.add(streamTestEvent{ID: 2, Stack: "db", Title: "not mine"})
	f.add(streamTestEvent{ID: 3, Title: "whole host"})
	srv, secret := streamServer(t, f, "web")
	lines := strings.Join(readStream(t, srv.URL+"/api/v1/events/stream", secret, nil), "\n")
	if !strings.Contains(lines, `"mine"`) || strings.Contains(lines, "not mine") || strings.Contains(lines, "whole host") {
		t.Errorf("%s", lines)
	}
}

func TestStreamNeedsAuth(t *testing.T) {
	f := &streamFixture{}
	srv, _ := streamServer(t, f)
	res, err := http.Get(srv.URL + "/api/v1/events/stream") //nolint:noctx // a test's short request
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("%d", res.StatusCode)
	}
}

func TestStreamsAreBounded(t *testing.T) {
	fastStream(t)
	streams.Store(maxStreams)
	t.Cleanup(func() { streams.Store(0) })
	f := &streamFixture{}
	srv, secret := streamServer(t, f)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/v1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("%d", res.StatusCode)
	}
}

func TestStreamEndsWhenTheClientGoes(t *testing.T) {
	fastStream(t)
	streamLifetime = time.Hour
	f := &streamFixture{}
	srv, secret := streamServer(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	if res, err := http.DefaultClient.Do(req); err == nil {
		_ = res.Body.Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for streams.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d streams still counted", streams.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
