// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"net/http"
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
	if err := s.TryStop(); err != nil {
		fail(w, http.StatusConflict, err.Error()+": stop the daemon when it has ended")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	// Serve's shutdown waits for this request to finish, so the answer arrives.
	go s.Stop()
}
