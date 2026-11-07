// SPDX-License-Identifier: AGPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Serraniel/nepfleet/internal/auth"
)

// Events are written by other processes too (apply, watch), so a stream looks at
// the store rather than being told. It ends after a while, as a token can be
// revoked meanwhile: clients reconnect with the last ID, and that signs in anew.
var (
	streamPoll      = 2 * time.Second
	streamHeartbeat = 25 * time.Second
	streamLifetime  = 10 * time.Minute
)

const (
	maxStreams        = 32
	streamWriteWindow = 30 * time.Second
	streamPage        = 100
)

var streams atomic.Int64

// eventID reads the ID out of an event in whatever type the store has.
func eventID(raw json.RawMessage) (int64, bool) {
	var e struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return 0, false
	}
	return e.ID, true
}

// streamEvents serves GET /api/v1/events/stream as server-sent events.
func streamEvents(o Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if o.Events == nil {
			http.Error(w, "no events", http.StatusServiceUnavailable)
			return
		}
		if streams.Add(1) > maxStreams {
			streams.Add(-1)
			w.Header().Set("Retry-After", "30")
			http.Error(w, "too many event streams", http.StatusServiceUnavailable)
			return
		}
		defer streams.Add(-1)

		// A reconnecting browser says where it was; so can anyone with ?after=.
		from := r.Header.Get("Last-Event-ID")
		if from == "" {
			from = r.URL.Query().Get("after")
		}
		after, _ := strconv.ParseInt(from, 10, 64)

		t, _ := TokenFrom(r.Context())
		allow := func(stack string) bool {
			if stack == "" {
				return len(t.Stacks) == 0
			}
			return t.Allows(auth.ScopeRead, stack)
		}

		rc := http.NewResponseController(w)
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-store")
		h.Set("X-Accel-Buffering", "no") // proxies must pass events on as they come
		w.WriteHeader(http.StatusOK)

		// Each write gets its own time: the server's limit is for short answers.
		send := func(text string) bool {
			if err := rc.SetWriteDeadline(time.Now().Add(streamWriteWindow)); err != nil {
				log.Printf("api: event stream: %v", err)
				return false
			}
			if _, err := fmt.Fprint(w, text); err != nil {
				return false
			}
			return rc.Flush() == nil
		}
		if !send(": connected\n\n") {
			return
		}

		poll := time.NewTicker(streamPoll)
		defer poll.Stop()
		beat := time.NewTicker(streamHeartbeat)
		defer beat.Stop()
		end := time.NewTimer(streamLifetime)
		defer end.Stop()
		for {
			evs, err := o.Events(after, streamPage, allow)
			if err != nil {
				log.Printf("api: event stream: %v", err)
				return
			}
			raw, err := json.Marshal(evs)
			if err != nil {
				return
			}
			var list []json.RawMessage
			if err := json.Unmarshal(raw, &list); err != nil {
				return
			}
			for _, item := range list {
				id, ok := eventID(item)
				if !ok {
					continue
				}
				// JSON from Marshal holds no raw newlines, so one data line is enough.
				if !send(fmt.Sprintf("id: %d\nevent: event\ndata: %s\n\n", id, item)) {
					return
				}
				after = id
			}
			select {
			case <-r.Context().Done():
				return
			case <-end.C:
				return
			case <-beat.C:
				if !send(": still here\n\n") {
					return
				}
			case <-poll.C:
			}
		}
	}
}
