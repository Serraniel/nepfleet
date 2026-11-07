// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
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
	if rec := get(all, "10.0.0.9"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after 10 failures: %d", rec.Code)
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
