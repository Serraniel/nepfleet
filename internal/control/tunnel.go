// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Serraniel/nepfleet/internal/remote"
)

// Tunnel states, as clients see them.
const (
	TunnelOff        = "off"
	TunnelConnecting = "connecting"
	TunnelUp         = "up"
	TunnelDown       = "down" // retrying
)

// tokenLifetime and tokenRenew: tokens are short-lived and kept in memory
// only, so there is nothing on disk to steal. Access to the remote comes from
// SSH each time.
const (
	tokenLifetime = "24h"
	tokenRenew    = 12 * time.Hour
)

// Connector reaches remotes; the daemon uses remote.SSH.
type Connector interface {
	MintToken(ctx context.Context, t remote.Target, label, lifetime string) (remote.Token, error)
	RevokeToken(ctx context.Context, t remote.Target, id string) error
	Forward(ctx context.Context, t remote.Target, socket string) error
}

// tunnel is one remote's connection.
type tunnel struct {
	mu     sync.Mutex
	state  string
	err    string
	token  remote.Token
	minted time.Time
	cancel context.CancelFunc
	done   chan struct{}
	socket string
	proxy  *httputil.ReverseProxy
}

func (t *tunnel) set(state, err string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state, t.err = state, err
}

func (t *tunnel) status() (state, err string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state, t.err
}

func (t *tunnel) secret() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.token.Secret
}

// startTunnel runs a remote's tunnel in the background. Call with s.mu held.
func (s *Server) startTunnel(name, address string) error {
	if s.tunnels == nil {
		s.tunnels = map[string]*tunnel{}
	}
	if _, ok := s.tunnels[name]; ok {
		return nil
	}
	target, err := remote.ParseTarget(address)
	if err != nil {
		return err
	}
	dir := filepath.Join(s.Dir, "tunnels")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &tunnel{state: TunnelConnecting, cancel: cancel, done: make(chan struct{}), socket: filepath.Join(dir, name+".sock")}
	t.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme, r.Out.URL.Host = "http", "remote"
			r.Out.Host = "remote"
			// The client has no token of its own: this is the only one sent.
			r.Out.Header.Set("Authorization", "Bearer "+t.secret())
		},
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", t.socket)
		}},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			log.Printf("control: %s: %v", name, err)
			fail(w, http.StatusBadGateway, "the remote does not answer")
		},
	}
	s.tunnels[name] = t
	go s.runTunnel(ctx, t, name, target)
	return nil
}

// stopTunnel ends a tunnel and revokes its token. Call without s.mu.
func (s *Server) stopTunnel(name string) {
	s.mu.Lock()
	t := s.tunnels[name]
	delete(s.tunnels, name)
	s.mu.Unlock()
	if t != nil {
		t.cancel()
		<-t.done
	}
}

// runTunnel keeps the tunnel up: a token, then ssh, again after any failure.
func (s *Server) runTunnel(ctx context.Context, t *tunnel, name string, target remote.Target) {
	defer close(t.done)
	defer func() {
		// The token dies with the tunnel; if the remote is gone, it expires.
		t.mu.Lock()
		id := t.token.ID
		t.token = remote.Token{}
		t.mu.Unlock()
		if id != "" {
			rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := s.Connector.RevokeToken(rctx, target, id); err != nil {
				log.Printf("control: %s: revoking its token: %v (it expires within a day)", name, err)
			}
		}
		_ = os.Remove(t.socket)
	}()
	backoff := s.retry()
	for ctx.Err() == nil {
		up, err := s.connectOnce(ctx, t, name, target)
		if ctx.Err() != nil {
			return
		}
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		t.set(TunnelDown, msg)
		if up {
			backoff = s.retry()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (s *Server) retry() time.Duration {
	if s.RetryAfter > 0 {
		return s.RetryAfter
	}
	return 2 * time.Second
}

// connectOnce runs one ssh connection until it ends, and reports whether the
// API answered through it.
func (s *Server) connectOnce(ctx context.Context, t *tunnel, name string, target remote.Target) (bool, error) {
	if err := s.renewToken(ctx, t, name, target); err != nil {
		return false, err
	}
	fctx, cancel := context.WithCancel(ctx)
	defer cancel()
	exited := make(chan error, 1)
	go func() { exited <- s.Connector.Forward(fctx, target, t.socket) }()

	up := false
	check := time.NewTicker(s.pollEvery())
	defer check.Stop()
	renew := time.NewTicker(tokenRenew)
	defer renew.Stop()
	for {
		select {
		case err := <-exited:
			return up, err
		case <-check.C:
			if healthy(ctx, t) {
				up = true
				t.set(TunnelUp, "")
			} else if state, _ := t.status(); state == TunnelUp {
				// ssh runs, but nothing answers behind it.
				t.set(TunnelDown, "NepFleet does not answer on the remote")
			}
		case <-renew.C:
			if err := s.renewToken(ctx, t, name, target); err != nil {
				log.Printf("control: %s: renewing its token: %v", name, err)
			}
		}
	}
}

func (s *Server) pollEvery() time.Duration {
	if s.PollEvery > 0 {
		return s.PollEvery
	}
	return 5 * time.Second
}

// renewToken mints a token, unless this one is fresh, and revokes the old one.
func (s *Server) renewToken(ctx context.Context, t *tunnel, name string, target remote.Target) error {
	t.mu.Lock()
	have, old := t.token.Secret != "" && time.Since(t.minted) < tokenRenew, t.token.ID
	t.mu.Unlock()
	if have {
		return nil
	}
	host, _ := os.Hostname()
	tok, err := s.Connector.MintToken(ctx, target, tokenLabel(host), tokenLifetime)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.token = tok
	t.minted = time.Now()
	t.mu.Unlock()
	if old != "" {
		if err := s.Connector.RevokeToken(ctx, target, old); err != nil {
			log.Printf("control: %s: revoking the old token: %v", name, err)
		}
	}
	return nil
}

// healthy asks the API through the tunnel; /healthz needs no token.
func healthy(ctx context.Context, t *tunnel) bool {
	c := http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", t.socket)
		}}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://remote/healthz", nil)
	if err != nil {
		return false
	}
	res, err := c.Do(req) //nolint:gosec // always the tunnel's socket
	if err != nil {
		return false
	}
	_ = res.Body.Close()
	return res.StatusCode < 300
}

// tokenLabel names the token after this machine, so the remote's token list
// says where it is used.
func tokenLabel(host string) string {
	b := []byte("control")
	if host != "" {
		b = append(b, '-')
	}
	for _, c := range []byte(host) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b = append(b, c)
		case c >= 'A' && c <= 'Z':
			b = append(b, c+32)
		default:
			b = append(b, '-')
		}
	}
	b = b[:min(len(b), 40)]
	for len(b) > 0 && b[len(b)-1] == '-' {
		b = b[:len(b)-1]
	}
	return string(b)
}

// connect turns a remote's tunnel on, and keeps it on across restarts.
func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	if s.Connector == nil {
		fail(w, http.StatusServiceUnavailable, "this daemon has no way to connect")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(r.PathValue("name"))
	if i < 0 {
		fail(w, http.StatusNotFound, "no such remote")
		return
	}
	s.remotes[i].Connect = true
	if err := s.save(); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.startTunnel(s.remotes[i].Name, s.remotes[i].Address); err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.viewLocked(s.remotes[i]))
}

func (s *Server) disconnect(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	i := s.index(name)
	if i < 0 {
		s.mu.Unlock()
		fail(w, http.StatusNotFound, "no such remote")
		return
	}
	s.remotes[i].Connect = false
	err := s.save()
	s.mu.Unlock()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.stopTunnel(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if i = s.index(name); i < 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, s.viewLocked(s.remotes[i]))
}

// viewLocked adds the tunnel's state, which is not saved. Call with s.mu held.
func (s *Server) viewLocked(r Remote) Remote {
	r.Tunnel, r.TunnelError = TunnelOff, ""
	if t := s.tunnels[r.Name]; t != nil {
		r.Tunnel, r.TunnelError = t.status()
	}
	return r
}

// apiProxy forwards /v1/remotes/{name}/api/... to the remote's /api/...,
// adding the token, so clients of the daemon never hold one.
func (s *Server) apiProxy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	t := s.tunnels[name]
	s.mu.Unlock()
	if t == nil {
		fail(w, http.StatusConflict, fmt.Sprintf("%s is not connected; use nepfleet remote connect %s", name, name))
		return
	}
	if state, _ := t.status(); state != TunnelUp {
		fail(w, http.StatusServiceUnavailable, name+" is "+state)
		return
	}
	// Only the remote's /api/: an encoded ".." must not climb out of it.
	if slices.Contains(strings.Split(r.PathValue("rest"), "/"), "..") {
		fail(w, http.StatusBadRequest, "bad path")
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/api/" + r.PathValue("rest")
	r2.URL.RawPath = ""
	t.proxy.ServeHTTP(w, r2)
}

// startSaved brings up the tunnels that were on; stopAll ends them all.
func (s *Server) startSaved() {
	if s.Connector == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.remotes {
		if r.Connect {
			if err := s.startTunnel(r.Name, r.Address); err != nil {
				log.Printf("control: %s: %v", r.Name, err)
			}
		}
	}
}

func (s *Server) stopAll() {
	s.mu.Lock()
	names := make([]string, 0, len(s.tunnels))
	for n := range s.tunnels {
		names = append(names, n)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, n := range names {
		wg.Go(func() { s.stopTunnel(n) })
	}
	wg.Wait()
}
