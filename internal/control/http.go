// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/api"
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

// CheckHTTPOrigin accepts what a browser or the desktop app sends as Origin.
func CheckHTTPOrigin(o string) error {
	// The desktop app's own origin on Linux and macOS (Windows uses
	// http://tauri.localhost).
	if o == "tauri://localhost" {
		return nil
	}
	return api.CheckOrigin(o)
}

// httpConfig is http.json: the port stays on across restarts of the daemon.
type httpConfig struct {
	Addr    string   `json:"addr"`
	Origins []string `json:"origins"`
}

func (s *Server) loadHTTPConfig() (*httpConfig, error) {
	raw, err := os.ReadFile(filepath.Join(s.Dir, "http.json")) //nolint:gosec // the daemon's own directory
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c httpConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("http.json: %w", err)
	}
	return &c, nil
}

// EnableHTTP opens the guarded port, or changes it, and remembers it. Origins
// add up: the desktop app and a browser UI can share the daemon.
func (s *Server) EnableHTTP(addr string, origins []string) error {
	if err := CheckHTTPAddr(addr); err != nil {
		return err
	}
	for _, o := range origins {
		if err := CheckHTTPOrigin(o); err != nil {
			return err
		}
	}
	s.httpMu.Lock()
	defer s.httpMu.Unlock()
	old, err := s.loadHTTPConfig()
	if err != nil {
		return err
	}
	all := slices.Clone(origins)
	if old != nil && old.Addr == addr {
		for _, o := range old.Origins {
			if !slices.Contains(all, o) {
				all = append(all, o)
			}
		}
	}
	slices.Sort(all)
	if err := s.listenHTTP(addr, all); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(httpConfig{Addr: addr, Origins: all}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.Dir, "http.json"), raw, 0o600)
}

// listenHTTP replaces the running port. Call with s.httpMu held.
func (s *Server) listenHTTP(addr string, origins []string) error {
	token, err := LoadHTTPToken(s.Dir)
	if err != nil {
		return err
	}
	if s.httpSrv != nil {
		_ = s.httpSrv.Close()
		s.httpSrv = nil
	}
	lc := net.ListenConfig{}
	l, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return err
	}
	// The plain handler: changing this port's rules is for the socket only, so
	// a page holding the token can't add its own origin.
	srv := &http.Server{Handler: Guard(s.Handler(), addr, token, origins), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	s.httpSrv = srv
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("control: http: %v", err)
		}
	}()
	return nil
}

// SocketHandler is Handler plus what only the owner-only socket may do.
func (s *Server) SocketHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", s.Handler())
	mux.HandleFunc("POST /v1/http", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Addr    string   `json:"addr"`
			Origins []string `json:"origins"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, `send {"addr": "127.0.0.1:7481", "origins": [...]}`)
			return
		}
		if err := s.EnableHTTP(req.Addr, req.Origins); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// startSavedHTTP reopens the port after a restart; a problem only warns, as the
// socket works regardless.
func (s *Server) startSavedHTTP() {
	s.httpMu.Lock()
	defer s.httpMu.Unlock()
	c, err := s.loadHTTPConfig()
	if err == nil && c != nil {
		err = s.listenHTTP(c.Addr, c.Origins)
	}
	if err != nil {
		log.Printf("control: http: %v", err)
	}
}

func (s *Server) stopHTTP() {
	s.httpMu.Lock()
	defer s.httpMu.Unlock()
	if s.httpSrv != nil {
		_ = s.httpSrv.Close()
		s.httpSrv = nil
	}
}
