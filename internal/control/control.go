// SPDX-License-Identifier: AGPL-3.0-or-later

// Package control is the local control daemon: it runs on a workstation, keeps
// running when its clients (CLI, web UI, desktop, TUI) close, and owns the
// connections to remote fleets.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Serraniel/nepfleet/internal/remote"
)

// Remote is a machine the daemon knows.
type Remote struct {
	Name    string         `json:"name"`
	Address string         `json:"address"`
	Added   time.Time      `json:"added"`
	Checked time.Time      `json:"checked,omitzero"`
	Report  *remote.Report `json:"report,omitempty"`
	// Next is what fits this machine, from the last probe.
	Next string `json:"next,omitempty"`
	// Error is why the last probe failed; Report stays from the one before.
	Error string `json:"error,omitempty"`
	// Connect: keep a tunnel up, also after a restart of the daemon.
	Connect bool `json:"connect,omitempty"`
	// Tunnel and TunnelError are the tunnel's state now; not meaningful in the
	// saved file.
	Tunnel      string `json:"tunnel,omitempty"`
	TunnelError string `json:"tunnelError,omitempty"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?$`)

// ProbeFunc looks at a machine; the daemon uses remote.Probe over SSH.
type ProbeFunc func(ctx context.Context, t remote.Target) (*remote.Report, error)

// Server is the daemon's state and API.
type Server struct {
	Dir     string // owner-only state directory
	Version string
	Probe   ProbeFunc
	// Connector, if set, lets the daemon keep tunnels to remotes.
	Connector Connector
	// RetryAfter and PollEvery tune the tunnels; tests shorten them.
	RetryAfter, PollEvery time.Duration

	mu      sync.Mutex
	remotes []Remote
	tunnels map[string]*tunnel
	started time.Time

	feedOnce  sync.Once
	feedState *feed

	httpMu      sync.Mutex
	httpSrv     *http.Server
	httpAddr    string
	httpOrigins atomic.Pointer[[]string]
}

// Load reads the state, or starts empty.
func (s *Server) Load() error {
	s.started = time.Now().UTC()
	raw, err := os.ReadFile(filepath.Join(s.Dir, "remotes.json")) //nolint:gosec // the daemon's own directory
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &s.remotes); err != nil {
		return fmt.Errorf("remotes.json: %w", err)
	}
	return nil
}

func (s *Server) save() error {
	raw, err := json.MarshalIndent(s.remotes, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".remotes-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.Dir, "remotes.json"))
}

// Handler is the API on the socket.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"version": s.Version, "pid": os.Getpid(), "started": s.started})
	})
	mux.HandleFunc("GET /v1/remotes", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := make([]Remote, 0, len(s.remotes))
		for _, rem := range s.remotes {
			out = append(out, s.viewLocked(rem))
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /v1/events", s.eventsList)
	mux.HandleFunc("POST /v1/events/seen", s.eventsSeen)
	mux.HandleFunc("POST /v1/remotes", s.add)
	mux.HandleFunc("POST /v1/remotes/{name}/refresh", s.refresh)
	mux.HandleFunc("POST /v1/remotes/{name}/connect", s.connect)
	mux.HandleFunc("POST /v1/remotes/{name}/disconnect", s.disconnect)
	mux.HandleFunc("/v1/remotes/{name}/api/{rest...}", s.apiProxy)
	mux.HandleFunc("DELETE /v1/remotes/{name}", func(w http.ResponseWriter, r *http.Request) {
		s.stopTunnel(r.PathValue("name"))
		s.mu.Lock()
		defer s.mu.Unlock()
		i := s.index(r.PathValue("name"))
		if i >= 0 {
			s.forgetFeed(r.PathValue("name"))
		}
		if i < 0 {
			fail(w, http.StatusNotFound, "no such remote")
			return
		}
		s.remotes = slices.Delete(s.remotes, i, i+1)
		if err := s.save(); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (s *Server) index(name string) int {
	return slices.IndexFunc(s.remotes, func(r Remote) bool { return r.Name == name })
}

func (s *Server) add(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, `send {"name": "...", "address": "ssh://..."}`)
		return
	}
	t, err := remote.ParseTarget(req.Address)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Name == "" {
		req.Name = t.Host
	}
	if !nameRe.MatchString(req.Name) {
		fail(w, http.StatusBadRequest, fmt.Sprintf("%q cannot be a remote's name: lowercase letters, digits, - and _", req.Name))
		return
	}
	s.mu.Lock()
	exists := s.index(req.Name) >= 0
	s.mu.Unlock()
	if exists {
		fail(w, http.StatusConflict, "a remote named "+req.Name+" exists")
		return
	}
	// A machine that can't be reached isn't added: a typo or an unknown host
	// key should show now.
	rep, err := s.Probe(r.Context(), t)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	now := time.Now().UTC()
	rem := Remote{Name: req.Name, Address: t.String(), Added: now, Checked: now, Report: rep, Next: rep.Next()}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index(req.Name) >= 0 {
		fail(w, http.StatusConflict, "a remote named "+req.Name+" exists")
		return
	}
	s.remotes = append(s.remotes, rem)
	if err := s.save(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rem)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	i := s.index(name)
	var address string
	if i >= 0 {
		address = s.remotes[i].Address
	}
	s.mu.Unlock()
	if i < 0 {
		fail(w, http.StatusNotFound, "no such remote")
		return
	}
	t, err := remote.ParseTarget(address)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	rep, perr := s.Probe(r.Context(), t)
	s.mu.Lock()
	defer s.mu.Unlock()
	if i = s.index(name); i < 0 {
		fail(w, http.StatusNotFound, "no such remote")
		return
	}
	s.remotes[i].Checked = time.Now().UTC()
	if perr != nil {
		s.remotes[i].Error = perr.Error()
	} else {
		s.remotes[i].Report, s.remotes[i].Next, s.remotes[i].Error = rep, rep.Next(), ""
	}
	if err := s.save(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.viewLocked(s.remotes[i]))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// ErrRunning means a daemon already answers on the socket.
var ErrRunning = errors.New("the control daemon is already running")

// Listen opens the owner-only socket. A leftover socket of a dead daemon is
// replaced; one that answers is left alone.
func Listen(socket string) (net.Listener, error) {
	dir := filepath.Dir(socket)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// The directory is what keeps other users out: refuse one they can enter.
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	// (Windows has no such modes; a user's own application data is private.)
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must be private to you (chmod 700)", dir)
	}
	if fi, err := os.Lstat(socket); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", socket)
		}
		if c, err := net.DialTimeout("unix", socket, time.Second); err == nil {
			_ = c.Close()
			return nil, ErrRunning
		}
		if err := os.Remove(socket); err != nil {
			return nil, err
		}
	}
	l, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// Serve runs the API until ctx ends: on the socket, and on the guarded loopback
// port for browsers if it was turned on.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	srv := &http.Server{Handler: s.SocketHandler(), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(l) }()
	s.startSavedHTTP()
	defer s.stopHTTP()
	s.startSaved()
	defer s.stopAll()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			log.Printf("control: %v", err)
			_ = srv.Close()
		}
		return nil
	}
}
