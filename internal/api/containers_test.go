// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

func TestContainersEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	read, _, _ := store.Create("read", auth.ScopeRead, nil, time.Hour)
	apply, _, _ := store.Create("apply", auth.ScopeApply, nil, time.Hour)
	db, _, _ := store.Create("db", auth.ScopeApply, []string{"db"}, time.Hour)
	var did []string
	h := Handler(Options{Tokens: store,
		Containers: func(_ context.Context, stack string) ([]Container, error) {
			if stack == "empty" {
				return nil, nil
			}
			return []Container{{Service: "web", Name: stack + "-web-1", State: "running", Health: "healthy", Image: "nginx:1.29", Restarts: 2}}, nil
		},
		ServiceAction: func(_ context.Context, stack, service, action, _ string) error {
			if stack == "swarm" && action != ServiceRestart {
				return ErrNotSupported
			}
			if service == "nope" {
				return ErrNotFound
			}
			did = append(did, action+" "+stack+"/"+service)
			return nil
		}})
	do := func(token, method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(read, http.MethodGet, "/api/v1/stacks/web/containers"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"restarts":2`) {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := do(read, http.MethodGet, "/api/v1/stacks/empty/containers"); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty: %s", rec.Body)
	}
	if rec := do(read, http.MethodPost, "/api/v1/stacks/web/services/web/restart"); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Errorf("a read token restarted: %d", rec.Code)
	}
	if rec := do(apply, http.MethodPost, "/api/v1/stacks/web/services/web/restart"); rec.Code != http.StatusNoContent {
		t.Errorf("restart: %d %s", rec.Code, rec.Body)
	}
	if rec := do(apply, http.MethodPost, "/api/v1/stacks/web/services/web/kill"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown action: %d", rec.Code)
	}
	if rec := do(apply, http.MethodPost, "/api/v1/stacks/web/services/-x/stop"); rec.Code != http.StatusNotFound {
		t.Errorf("odd service name: %d", rec.Code)
	}
	if rec := do(apply, http.MethodPost, "/api/v1/stacks/web/services/nope/stop"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown service: %d", rec.Code)
	}
	if rec := do(apply, http.MethodPost, "/api/v1/stacks/swarm/services/web/stop"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("swarm stop: %d", rec.Code)
	}
	if rec := do(db, http.MethodPost, "/api/v1/stacks/web/services/web/stop"); rec.Code != http.StatusNotFound {
		t.Errorf("a token for db stopped web: %d", rec.Code)
	}
	if strings.Join(did, ";") != "restart web/web" {
		t.Errorf("done: %v", did)
	}
}

func TestLogsEndpoint(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	read, _, _ := store.Create("read", auth.ScopeRead, nil, time.Hour)
	edit, _, _ := store.Create("edit", auth.ScopeEdit, nil, time.Hour)
	var asked int
	h := Handler(Options{Tokens: store, Logs: func(_ context.Context, stack, container string, lines int) (string, error) {
		if container == "other-web-1" {
			return "", ErrNotFound
		}
		asked = lines
		return "2026-11-15T10:00:00Z hello from " + stack + "\n", nil
	}})
	get := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(read, "/api/v1/stacks/web/containers/web-web-1/logs"); rec.Code == http.StatusOK {
		t.Error("a read token got logs")
	}
	rec := get(edit, "/api/v1/stacks/web/containers/web-web-1/logs")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hello from web") || asked != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("logs: %d %q (lines %d)", rec.Code, rec.Body, asked)
	}
	if get(edit, "/api/v1/stacks/web/containers/web-web-1/logs?lines=50"); asked != 50 {
		t.Errorf("lines: %d", asked)
	}
	for _, q := range []string{"0", "-1", "99999", "x"} {
		if rec := get(edit, "/api/v1/stacks/web/containers/web-web-1/logs?lines="+q); rec.Code != http.StatusBadRequest {
			t.Errorf("lines=%s: %d", q, rec.Code)
		}
	}
	if rec := get(edit, "/api/v1/stacks/web/containers/other-web-1/logs"); rec.Code != http.StatusNotFound {
		t.Errorf("another stack's container: %d", rec.Code)
	}
}

func TestDNSRecordsEndpoint(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	admin, _, _ := store.Create("admin", auth.ScopeAdmin, nil, time.Hour)
	edit, _, _ := store.Create("edit", auth.ScopeEdit, nil, time.Hour)
	var zone string
	h := Handler(Options{Tokens: store, DNSRecords: func(_ context.Context, z, _ string) (DNSRecordsResult, error) {
		if z == "nocaddy.org" {
			return DNSRecordsResult{}, InvalidError{Reason: "no Caddy stack runs on this host"}
		}
		zone = z
		return DNSRecordsResult{Stack: "caddy", Commit: "abc"}, nil
	}})
	post := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/dns-records", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(edit, `{"zone":"example.org"}`); rec.Code == http.StatusOK {
		t.Error("an edit token turned DNS records on")
	}
	if rec := post(admin, `{"zone":"example.org"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"stack":"caddy"`) || zone != "example.org" {
		t.Errorf("admin: %d %s", rec.Code, rec.Body)
	}
	if rec := post(admin, `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no zone: %d", rec.Code)
	}
	if rec := post(admin, `{"zone":"nocaddy.org"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "no Caddy") {
		t.Errorf("refused: %d %s", rec.Code, rec.Body)
	}
}

func TestFollowLogs(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	edit, _, _ := store.Create("edit", auth.ScopeEdit, nil, time.Hour)
	release := make(chan struct{})
	h := Handler(Options{Tokens: store, FollowLogs: func(ctx context.Context, _, container string, _ int, out io.Writer) error {
		if container == "other-1" {
			return ErrNotFound
		}
		_, _ = out.Write([]byte("first\n"))
		select {
		case <-release:
		case <-ctx.Done():
			return nil
		}
		_, _ = out.Write([]byte("second\n"))
		return nil
	}})
	srv := httptest.NewServer(h)
	defer srv.Close()
	get := func(path string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+edit)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	status := func(path string) int {
		resp := get(path)
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}
	resp := get("/api/v1/stacks/web/containers/web-web-1/logs?follow=1")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("follow: %d", resp.StatusCode)
	}
	// The first line arrives before the second is written: streamed, not buffered.
	r := bufio.NewReader(resp.Body)
	if line, err := r.ReadString('\n'); err != nil || line != "first\n" {
		t.Fatalf("first: %q %v", line, err)
	}
	close(release)
	if line, _ := r.ReadString('\n'); line != "second\n" {
		t.Errorf("second: %q", line)
	}
	if code := status("/api/v1/stacks/web/containers/other-1/logs?follow=1"); code != http.StatusNotFound {
		t.Errorf("unknown container: %d", code)
	}
	old := maxLogFollows
	maxLogFollows = 0
	t.Cleanup(func() { maxLogFollows = old })
	if code := status("/api/v1/stacks/web/containers/web-web-1/logs?follow=1"); code != http.StatusServiceUnavailable {
		t.Errorf("over the limit: %d", code)
	}
}
