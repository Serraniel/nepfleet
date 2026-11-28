// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

// APILevel is the control API's level in GET /v1/info. Clients that need
// something newer tell the user to update nepfleet instead of failing oddly.
// 2: POST /v1/restart, and the daemon follows updates of its binary.
const APILevel = 2

// ErrBusy means a setup runs, and stopping would cut it off halfway.
var ErrBusy = errors.New("a setup runs")

// keptListener stays open when the daemon hands over to the binary installed
// now: Close then only ends Accept, and the next daemon accepts on it. Clients
// connecting meanwhile wait in the backlog instead of being refused.
type keptListener struct {
	net.Listener
	s *Server
}

func (l keptListener) Close() error {
	if l.s.handingOff.Load() {
		if d, ok := l.Listener.(interface{ SetDeadline(time.Time) error }); ok {
			return d.SetDeadline(time.Unix(1, 0))
		}
	}
	return l.Listener.Close()
}

// TryStop begins stopping unless a setup runs: from then on no setup starts.
func (s *Server) TryStop() error {
	s.Setup.mu.Lock()
	defer s.Setup.mu.Unlock()
	var busy []string
	for name, j := range s.Setup.jobs {
		if j.snapshot().State == "running" {
			busy = append(busy, name)
		}
	}
	for name := range s.Setup.starting {
		busy = append(busy, name)
	}
	if len(busy) > 0 {
		slices.Sort(busy)
		return fmt.Errorf("%w on %s", ErrBusy, strings.Join(slices.Compact(busy), ", "))
	}
	s.Setup.stopping = true
	return nil
}

// HandOff makes Serve end with its listeners still open, for Listeners.
func (s *Server) HandOff() { s.handingOff.Store(true) }

// Listeners are the socket and the port (with its address) after Serve ended
// for a handover: duplicates the next daemon inherits.
func (s *Server) Listeners() (sock, port *os.File, addr string, err error) {
	file := func(l net.Listener) (*os.File, error) {
		f, ok := l.(interface{ File() (*os.File, error) })
		if !ok {
			return nil, fmt.Errorf("can't pass on a %T", l)
		}
		return f.File()
	}
	if s.sockL == nil {
		return nil, nil, "", errors.New("no socket to pass on")
	}
	if sock, err = file(s.sockL); err != nil {
		return nil, nil, "", err
	}
	if s.keptHTTP != nil {
		if port, err = file(s.keptHTTP); err != nil {
			_ = sock.Close()
			return nil, nil, "", err
		}
		addr = s.keptHTTPAddr
	}
	return sock, port, addr, nil
}

// InheritHTTP hands the port of the daemon before to this one: the saved
// address takes it instead of listening anew, so it never closes.
func (s *Server) InheritHTTP(l net.Listener, addr string) {
	s.httpMu.Lock()
	defer s.httpMu.Unlock()
	s.inheritedHTTP, s.inheritedHTTPAddr = l, addr
}

// restart is POST /v1/restart: the daemon hands over to the binary installed
// now, keeping its socket, its port and the unlocked keys. It refuses while a
// setup runs, as a stop does.
func (s *Server) restart(w http.ResponseWriter, _ *http.Request) {
	if s.Restart == nil {
		fail(w, http.StatusNotImplemented, "this daemon can't restart itself here; stop it and start it again")
		return
	}
	if err := s.Restart(); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, ErrBusy) {
			code = http.StatusConflict
		}
		fail(w, code, err.Error()+": restart the daemon when it has ended")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
