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

// ErrNoHandoff means the binary installed now can't take over in place (it
// doesn't run, or is from before handovers): stop and start instead.
var ErrNoHandoff = errors.New("the nepfleet installed now can't take over in place")

// handoffGrace is how long a handover lets requests running finish.
const handoffGrace = 10 * time.Second

// Proxying is how many requests run through tunnels now: an automatic
// restart waits for none, so no event stream or apply is cut off.
func (s *Server) Proxying() int64 { return s.proxying.Load() }

// ResumeAfterHandoff is for a handover that failed (exec refused): this
// daemon carries on with the same listeners, which stayed open, and Serve can
// run again. GET /v1/info says why it failed.
func (s *Server) ResumeAfterHandoff(why error) {
	s.handingOff.Store(false)
	s.Setup.mu.Lock()
	s.Setup.stopping = false
	s.Setup.mu.Unlock()
	noDeadline := func(l net.Listener) {
		if d, ok := l.(interface{ SetDeadline(time.Time) error }); ok {
			_ = d.SetDeadline(time.Time{})
		}
	}
	if s.sockL != nil {
		noDeadline(s.sockL)
	}
	s.httpMu.Lock()
	if s.keptHTTP != nil {
		noDeadline(s.keptHTTP)
		s.inheritedHTTP, s.inheritedHTTPAddr = s.keptHTTP, s.keptHTTPAddr
		s.keptHTTP, s.keptHTTPAddr = nil, ""
	}
	s.httpMu.Unlock()
	msg := why.Error()
	s.handoffErr.Store(&msg)
}

// handoffError is why the last handover failed, "" if it didn't.
func (s *Server) handoffError() string {
	if p := s.handoffErr.Load(); p != nil {
		return *p
	}
	return ""
}

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
	empty := ""
	s.handoffErr.Store(&empty) // a new try
	if err := s.Restart(); err != nil {
		switch {
		case errors.Is(err, ErrBusy):
			fail(w, http.StatusConflict, err.Error()+": restart the daemon when it has ended")
		case errors.Is(err, ErrNoHandoff):
			fail(w, http.StatusNotImplemented, err.Error())
		default:
			fail(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
