// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"context"
	"net"
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

func TestEnableHTTP(t *testing.T) {
	dir := t.TempDir()
	s := &Server{Dir: dir}
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	defer s.stopHTTP()
	h := s.SocketHandler()
	post := func(handler http.Handler, body string) int {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/http", strings.NewReader(body)))
		return rec.Code
	}
	for body, want := range map[string]int{
		`{"addr":"0.0.0.0:17492","origins":[]}`:                          400,
		`{"addr":"127.0.0.1:17492","origins":["*"]}`:                     400,
		`{"addr":"127.0.0.1:17492","origins":["tauri://evil"]}`:          400,
		`{"addr":"127.0.0.1:17492","origins":["tauri://localhost"]}`:     204,
		`{"addr":"127.0.0.1:17492","origins":["http://localhost:5173"]}`: 204,
	} {
		if got := post(h, body); got != want {
			t.Errorf("%s: %d, want %d", body, got, want)
		}
	}
	// A page holding the token must not reach this: only the socket has it.
	if got := post(s.Handler(), `{"addr":"127.0.0.1:17492","origins":["https://evil.example"]}`); got == 204 {
		t.Error("the HTTP port must not change its own rules")
	}
	c, err := s.loadHTTPConfig()
	if err != nil || c == nil || c.Addr != "127.0.0.1:17492" {
		t.Fatalf("saved: %+v %v", c, err)
	}
	fi, err := os.Stat(filepath.Join(dir, "http.json"))
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Errorf("http.json: %v %v", fi, err)
	}
	token, err := LoadHTTPToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	// It really listens, and comes back after a restart.
	get := func() int {
		req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:17492/v1/info", nil) //nolint:noctx // a test
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req) //nolint:gosec // a test's own port
		if err != nil {
			return 0
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	if got := get(); got != 200 {
		t.Errorf("port: %d", got)
	}
	s.stopHTTP()
	s2 := &Server{Dir: dir}
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	s2.startSavedHTTP()
	defer s2.stopHTTP()
	if got := get(); got != 200 {
		t.Errorf("after a restart: %d", got)
	}
}

func TestEnableHTTPOrigins(t *testing.T) {
	s := &Server{Dir: t.TempDir()}
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	defer s.stopHTTP()
	token, err := LoadHTTPToken(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	from := func(addr, origin string) int {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/info", nil) //nolint:noctx // a test
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Origin", origin)
		res, err := http.DefaultClient.Do(req) //nolint:gosec // a test's own port
		if err != nil {
			return 0
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	const a, b = "127.0.0.1:17494", "127.0.0.1:17495"
	// Quickly in a row: a new listener in between would race for the address.
	for range 20 {
		if err := s.EnableHTTP(a, []string{"http://one.test"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := from(a, "http://two.test"); got != http.StatusForbidden {
		t.Errorf("not allowed yet: %d", got)
	}
	if err := s.EnableHTTP(a, []string{"http://two.test"}); err != nil {
		t.Fatal(err)
	}
	if got := from(a, "http://two.test"); got != 200 {
		t.Errorf("allowed without a new listener: %d", got)
	}
	if got := from(a, "http://one.test"); got != 200 {
		t.Errorf("origins add up: %d", got)
	}
	// A new address that can't be opened leaves the working one alone.
	lc := net.ListenConfig{}
	busy, err := lc.Listen(context.Background(), "tcp", b)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnableHTTP(b, nil); err == nil {
		t.Error("a taken address must fail")
	}
	_ = busy.Close()
	if got := from(a, "http://one.test"); got != 200 {
		t.Errorf("the old port must survive: %d", got)
	}
}
