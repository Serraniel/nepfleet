// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// stop is POST /v1/stop: the daemon ends, so the next start (nepfleet control
// restart, or the app) runs the binary installed now. Tunnels come back after
// that start, as they do after any restart. It refuses while a setup runs, as
// stopping would cut it off halfway.
func (s *Server) stop(w http.ResponseWriter, _ *http.Request) {
	if s.Stop == nil {
		fail(w, http.StatusServiceUnavailable, "this daemon can't be stopped over its API")
		return
	}
	s.Setup.mu.Lock()
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
		s.Setup.mu.Unlock()
		slices.Sort(busy)
		fail(w, http.StatusConflict, fmt.Sprintf("a setup runs on %s: stop the daemon when it has ended", strings.Join(slices.Compact(busy), ", ")))
		return
	}
	s.Setup.stopping = true
	s.Setup.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
	// Serve's shutdown waits for this request to finish, so the answer arrives.
	go s.Stop()
}
