// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
