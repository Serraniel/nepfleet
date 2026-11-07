// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// HTTPTokenPrefix marks the daemon's own token, so secret scanners find it.
const HTTPTokenPrefix = "nfc_"

// LoadHTTPToken reads the token browsers use, creating it on first use. The
// file is owner-only: reading it is what shows a client is this user.
func LoadHTTPToken(dir string) (string, error) {
	path := filepath.Join(dir, "http.token")
	raw, err := os.ReadFile(path) //nolint:gosec // the daemon's own directory
	if err == nil {
		t := strings.TrimSpace(string(raw))
		if !strings.HasPrefix(t, HTTPTokenPrefix) || len(t) < 40 {
			return "", fmt.Errorf("%s is damaged; delete it to get a new token", path)
		}
		return t, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	t := HTTPTokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // the daemon's own directory
	if errors.Is(err, os.ErrExist) {
		return LoadHTTPToken(dir) // another process won the race
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(t + "\n"); err != nil {
		return "", err
	}
	return t, nil
}

// CheckHTTPAddr accepts loopback addresses only: this port reaches every
// connected fleet, so it must not be on the network.
func CheckHTTPAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--http %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--http %q: only a loopback address such as 127.0.0.1:7481; this port reaches all your fleets", addr)
	}
	return nil
}

// Guard protects the API when it is reachable over HTTP, where any web page
// the user opens can send requests to it:
//   - Host must be the loopback address itself, which stops DNS rebinding;
//   - browsers may only call from exact allowed origins;
//   - every request needs the token, which a page can't read from a file.
func Guard(next http.Handler, addr, token string, origins []string) http.Handler {
	_, port, _ := net.SplitHostPort(addr)
	hosts := []string{net.JoinHostPort("127.0.0.1", port), net.JoinHostPort("localhost", port), net.JoinHostPort("::1", port)}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(hosts, r.Host) {
			fail(w, http.StatusForbidden, "wrong host")
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		if o := r.Header.Get("Origin"); o != "" {
			if !slices.Contains(origins, o) {
				fail(w, http.StatusForbidden, "origin not allowed")
				return
			}
			h.Set("Access-Control-Allow-Origin", o)
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE")
			h.Set("Access-Control-Max-Age", "600")
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="nepfleet-control"`)
			fail(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}
