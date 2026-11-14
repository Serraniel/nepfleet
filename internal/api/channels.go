// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
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
			logf("api: test message to %s failed: %s", name, reason)
			http.Error(w, "sending failed: "+reason, http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	// Where every alert goes, and a URL with tokens: admin only, like tests.
	mux.HandleFunc("PUT /api/v1/notifications/channels/{name}", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.SetChannel == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		name := r.PathValue("name")
		if !channelName.MatchString(name) {
			http.Error(w, "a channel name is 1 to 64 letters, digits, dots, dashes or underscores", http.StatusBadRequest)
			return
		}
		var req struct {
			URL         string `json:"url"`
			MinSeverity string `json:"minSeverity"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.URL == "" {
			http.Error(w, `send {"url": "<shoutrrr URL>", "minSeverity": "info|warning|error"}`, http.StatusBadRequest)
			return
		}
		if req.MinSeverity != "" && req.MinSeverity != "info" && req.MinSeverity != "warning" && req.MinSeverity != "error" {
			http.Error(w, "minSeverity is info, warning or error", http.StatusBadRequest)
			return
		}
		t, _ := TokenFrom(r.Context())
		commit, err := o.SetChannel(r.Context(), name, req.URL, req.MinSeverity, t.Name)
		if channelErr(w, err) {
			return
		}
		logf("api: %s set notification channel %s (token %s)", t.Name, name, t.ID)
		writeJSON(w, map[string]string{"commit": commit})
	}))
	mux.HandleFunc("DELETE /api/v1/notifications/channels/{name}", requireAuth(o, l, auth.ScopeAdmin, func(w http.ResponseWriter, r *http.Request) {
		if o.RemoveChannel == nil {
			http.Error(w, "no fleet", http.StatusServiceUnavailable)
			return
		}
		name := r.PathValue("name")
		if !channelName.MatchString(name) {
			http.Error(w, ErrNoChannel.Error(), http.StatusNotFound)
			return
		}
		t, _ := TokenFrom(r.Context())
		commit, err := o.RemoveChannel(r.Context(), name, t.Name)
		if channelErr(w, err) {
			return
		}
		logf("api: %s removed notification channel %s (token %s)", t.Name, name, t.ID)
		writeJSON(w, map[string]string{"commit": commit})
	}))
}

// channelErr answers a channel change's error; the reasons never carry the
// URL (it holds tokens).
func channelErr(w http.ResponseWriter, err error) bool {
	var invalid InvalidError
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNoChannel):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrBusy), errors.Is(err, ErrUncommitted):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.As(err, &invalid):
		http.Error(w, redactURLs(err.Error()), http.StatusBadRequest)
	default:
		logf("api: channel: %s", redactURLs(err.Error()))
		http.Error(w, "the channel couldn't be saved; see the server log", http.StatusInternalServerError)
	}
	return true
}
