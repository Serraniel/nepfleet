// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"errors"
	"log"
	"net/http"
	"regexp"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// Channels are the notification channels of fleet.yaml, by name only: their
// URLs hold tokens.
type Channels struct {
	Channels []string `json:"channels"`
}

// ErrNoChannel means fleet.yaml has no channel of that name.
var ErrNoChannel = errors.New("no channel of that name in fleet.yaml")

var channelName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// anyURL matches URLs in an error, which for channels carry tokens.
var anyURL = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"']+`)

// redactURLs keeps an error's reason and drops the URLs in it.
func redactURLs(s string) string { return anyURL.ReplaceAllString(s, "<url>") }

func registerChannels(mux *http.ServeMux, o Options, l *limiter) {
	mux.HandleFunc("GET /api/v1/notifications/channels", requireAuth(o, l, auth.ScopeRead, func(w http.ResponseWriter, _ *http.Request) {
		if o.Channels == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		cs, err := o.Channels()
		if writeErr(w, err) {
			return
		}
		if cs == nil {
			cs = []string{}
		}
		writeJSON(w, Channels{Channels: cs})
	}))
	mux.HandleFunc("POST /api/v1/notifications/channels/{name}/test", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.TestChannel == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		name := r.PathValue("name")
		if !channelName.MatchString(name) {
			http.Error(w, ErrNoChannel.Error(), http.StatusNotFound)
			return
		}
		err := o.TestChannel(r.Context(), name)
		switch {
		case errors.Is(err, ErrNoChannel):
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		case err != nil:
			reason := redactURLs(err.Error())
			log.Printf("api: test message to %s failed: %s", name, reason) //nolint:gosec // a checked name; URLs removed
			http.Error(w, "sending failed: "+reason, http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}
