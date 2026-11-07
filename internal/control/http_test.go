// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHTTPToken(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadHTTPToken(dir)
	if err != nil || !strings.HasPrefix(a, HTTPTokenPrefix) {
		t.Fatal(a, err)
	}
	b, err := LoadHTTPToken(dir)
	if err != nil || a != b {
		t.Errorf("the token must stay: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "http.token"))
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Errorf("mode: %v %v", fi, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "http.token"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHTTPToken(dir); err == nil {
		t.Error("a damaged token must not be used")
	}
}

func TestCheckHTTPAddr(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:7481": true, "[::1]:7481": true, ":7481": false, "0.0.0.0:7481": false,
		"192.168.1.2:7481": false, "localhost:7481": false, "nas:7481": false, "x": false,
	} {
		if err := CheckHTTPAddr(addr); (err == nil) != ok {
			t.Errorf("%s: %v", addr, err)
		}
	}
}

func TestGuard(t *testing.T) {
	const token = "nfc_test"
	reached := 0
	h := Guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}), "127.0.0.1:7481", token, []string{"http://localhost:5173"})
	do := func(method, host, origin, auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/v1/remotes", nil)
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	const good = "127.0.0.1:7481"
	for _, c := range []struct {
		name, method, host, origin, auth string
		want                             int
	}{
		{"valid", http.MethodGet, good, "", token, 200},
		{"valid from the UI", http.MethodGet, good, "http://localhost:5173", token, 200},
		{"localhost", http.MethodGet, "localhost:7481", "", token, 200},
		{"no token", http.MethodGet, good, "", "", 401},
		{"wrong token", http.MethodGet, good, "", "nfc_other", 401},
		{"DNS rebinding", http.MethodGet, "evil.example:7481", "", token, 403},
		{"other port", http.MethodGet, "127.0.0.1:80", "", token, 403},
		{"other web page, even with the token", http.MethodPost, good, "https://evil.example", token, 403},
		{"preflight", http.MethodOptions, good, "http://localhost:5173", "", 204},
		{"preflight from another page", http.MethodOptions, good, "https://evil.example", "", 403},
	} {
		before := reached
		rec := do(c.method, c.host, c.origin, c.auth)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.want)
		}
		if c.want != 200 && reached != before {
			t.Errorf("%s: reached the API", c.name)
		}
	}
}
