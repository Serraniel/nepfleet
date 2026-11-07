// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

func TestCORSAllowlist(t *testing.T) {
	h := Handler(Options{AllowedOrigins: []string{"https://ui.example"}})
	for origin, want := range map[string]string{
		"https://ui.example":   "https://ui.example",
		"https://evil.example": "",
		"*":                    "",
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/info", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("origin %q: allow-origin = %q, want %q", origin, got, want)
		}
	}
}

func TestInfo(t *testing.T) {
	h := Handler(Options{Info: Info{Name: "home", Version: "dev", APIVersion: "v1"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/info", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("code=%d headers=%v", rec.Code, rec.Header())
	}
}

func TestStacksNeedToken(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	all, _, err := store.Create("all", auth.ScopeRead, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	webOnly, _, _ := store.Create("web", auth.ScopeRead, []string{"web"}, time.Hour)
	h := Handler(Options{Tokens: store, Stacks: func() ([]StackInfo, error) {
		return []StackInfo{{Name: "web"}, {Name: "db"}}, nil
	}})
	get := func(token, addr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks", nil)
		req.RemoteAddr = addr + ":1234"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("", "10.0.0.1"); rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("no token: %d", rec.Code)
	}
	if rec := get(all, "10.0.0.1"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"db"`) {
		t.Errorf("all stacks: %d %s", rec.Code, rec.Body)
	}
	var got []StackInfo
	rec := get(webOnly, "10.0.0.1")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0].Name != "web" {
		t.Errorf("a token limited to web sees %v (%v)", got, err)
	}
	// Ten wrong tokens from one address lock it out for a while; others are fine.
	for range 10 {
		get("nft_wrong", "10.0.0.9")
	}
	if rec := get("nft_wrong", "10.0.0.9"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after 10 failures: %d", rec.Code)
	}
	if rec := get(all, "10.0.0.9"); rec.Code != http.StatusOK {
		t.Errorf("a valid token gets in from a locked-out address: %d", rec.Code)
	}
	if rec := get(all, "10.0.0.1"); rec.Code != http.StatusOK {
		t.Errorf("another address: %d", rec.Code)
	}
}

func TestClientAddr(t *testing.T) {
	_, proxy, _ := net.ParseCIDR("172.16.0.0/12")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "172.18.0.5:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.7")
	if got := clientAddr(req, []*net.IPNet{proxy}); got != "198.51.100.7" {
		t.Errorf("behind a trusted proxy: %s", got)
	}
	if got := clientAddr(req, nil); got != "172.18.0.5" {
		t.Errorf("without trusted proxies the header is ignored: %s", got)
	}
	// A client-sent header line before the proxy's own line counts for nothing.
	req.Header.Del("X-Forwarded-For")
	req.Header.Add("X-Forwarded-For", "6.6.6.6")
	req.Header.Add("X-Forwarded-For", "198.51.100.7")
	if got := clientAddr(req, []*net.IPNet{proxy}); got != "198.51.100.7" {
		t.Errorf("several header lines: %s", got)
	}
	// A chain of trusted proxies (CDN → Caddy) is walked to the client.
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 198.51.100.7, 172.20.0.9")
	if got := clientAddr(req, []*net.IPNet{proxy}); got != "198.51.100.7" {
		t.Errorf("proxy chain: %s", got)
	}
	req.RemoteAddr = "203.0.113.50:1"
	if got := clientAddr(req, []*net.IPNet{proxy}); got != "203.0.113.50" {
		t.Errorf("an untrusted sender's header is ignored: %s", got)
	}
}

func TestLimiterBounded(t *testing.T) {
	l := newLimiter()
	for i := range maxTracked + 50 {
		l.fail(fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255))
	}
	if len(l.fails) > maxTracked {
		t.Errorf("tracked %d addresses", len(l.fails))
	}
}

func TestStackEndpoints(t *testing.T) {
	store := &auth.Store{Dir: t.TempDir()}
	all, _, err := store.Create("all", auth.ScopeRead, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	webOnly, _, _ := store.Create("web", auth.ScopeRead, []string{"web"}, time.Hour)
	h := Handler(Options{Tokens: store,
		Plan: func(_ context.Context, s string) (any, error) {
			switch s {
			case "web", "db":
				return map[string]string{"stack": s}, nil
			case "broken":
				return nil, errors.New("/srv/secret/path: git failed")
			}
			return nil, ErrNotFound
		},
		History: func(_ context.Context, s string, n int) ([]HistoryEntry, error) {
			return []HistoryEntry{{Commit: "abc", Subject: fmt.Sprintf("%s %d", s, n), Applied: true}}, nil
		},
	})
	get := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get(all, "/api/v1/stacks/db/plan"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"db"`) {
		t.Errorf("plan: %d %s", rec.Code, rec.Body)
	}
	if rec := get(webOnly, "/api/v1/stacks/db/plan"); rec.Code != http.StatusNotFound {
		t.Errorf("a token limited to web must not see db: %d", rec.Code)
	}
	if rec := get(all, "/api/v1/stacks/nope/plan"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown stack: %d", rec.Code)
	}
	if rec := get(all, "/api/v1/stacks/broken/plan"); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "/srv") {
		t.Errorf("errors must not leak internals: %d %s", rec.Code, rec.Body)
	}
	if rec := get(all, "/api/v1/stacks/Bad..Name/plan"); rec.Code != http.StatusNotFound {
		t.Errorf("invalid name: %d", rec.Code)
	}
	if rec := get(webOnly, "/api/v1/stacks/web/history?n=5"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"web 5"`) {
		t.Errorf("history: %d %s", rec.Code, rec.Body)
	}
	if rec := get("", "/api/v1/stacks/web/history"); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", rec.Code)
	}
}
