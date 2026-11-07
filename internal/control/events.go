// SPDX-License-Identifier: AGPL-3.0-or-later

package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Serraniel/nepfleet/internal/events"
)

// feedState is what the daemon remembers about the feeds: how far it read each
// fleet, and how far the user has seen.
type feedState struct {
	Cursors map[string]int64 `json:"cursors"`
	Seen    int64            `json:"seen"`
}

// feed merges the events of all connected fleets into one local store.
type feed struct {
	mu    sync.Mutex
	dir   string
	store *events.Store
	state feedState
}

func (s *Server) feed() *feed {
	s.feedOnce.Do(func() {
		dir := filepath.Join(s.Dir, "events")
		f := &feed{dir: dir, store: &events.Store{Dir: dir}, state: feedState{Cursors: map[string]int64{}}}
		raw, err := os.ReadFile(filepath.Join(dir, "state.json")) //nolint:gosec // the daemon's own directory
		if err == nil {
			if err := json.Unmarshal(raw, &f.state); err != nil {
				log.Printf("control: events state: %v", err)
			}
			if f.state.Cursors == nil {
				f.state.Cursors = map[string]int64{}
			}
		}
		s.feedState = f
	})
	return s.feedState
}

func (f *feed) saveLocked() error {
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(f.state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.dir, ".state-*")
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
	return os.Rename(tmp.Name(), filepath.Join(f.dir, "state.json"))
}

// feedPage is how many events one request asks for.
const feedPage = 200

// maxEventMessage bounds what a remote can make the daemon store per event.
const maxEventMessage = 8 << 10

// fetchEvents asks the remote through the tunnel.
func fetchEvents(ctx context.Context, t *tunnel, after int64) ([]events.Event, error) {
	u := "http://remote/api/v1/events?limit=" + strconv.Itoa(feedPage) + "&after=" + url.QueryEscape(strconv.FormatInt(after, 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.secret())
	res, err := t.client().Do(req) //nolint:gosec // always the tunnel's socket
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	var out []events.Event
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// pollFeed brings a fleet's new events in. A fleet seen for the first time
// starts from now: its history isn't news. An older server without the feed
// only means no events from it.
func (s *Server) pollFeed(ctx context.Context, t *tunnel, name string) {
	f := s.feed()
	f.mu.Lock()
	cursor, known := f.state.Cursors[name]
	f.mu.Unlock()
	for ctx.Err() == nil {
		got, err := fetchEvents(ctx, t, cursor)
		if err != nil {
			return
		}
		if len(got) == 0 {
			break
		}
		for _, e := range got {
			if e.ID <= cursor {
				return // a remote must not move the feed backwards or repeat itself
			}
			cursor = e.ID
			if !known {
				continue
			}
			e.Fleet = name
			if len(e.Message) > maxEventMessage {
				cut := maxEventMessage
				for cut > 0 && !utf8.RuneStart(e.Message[cut]) {
					cut--
				}
				e.Message = e.Message[:cut] + "…"
			}
			if _, err := f.store.Add(e); err != nil {
				log.Printf("control: events: %v", err)
				return
			}
		}
		f.mu.Lock()
		f.state.Cursors[name] = cursor
		err = f.saveLocked()
		f.mu.Unlock()
		if err != nil {
			log.Printf("control: events: %v", err)
			return
		}
		if len(got) < feedPage {
			break
		}
	}
	if !known {
		f.mu.Lock()
		if _, ok := f.state.Cursors[name]; !ok {
			f.state.Cursors[name] = cursor
			if err := f.saveLocked(); err != nil {
				log.Printf("control: events: %v", err)
			}
		}
		f.mu.Unlock()
	}
}

// forgetFeed drops a removed remote's place, so adding it again starts fresh.
func (s *Server) forgetFeed(name string) {
	f := s.feed()
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.state.Cursors[name]; ok {
		delete(f.state.Cursors, name)
		if err := f.saveLocked(); err != nil {
			log.Printf("control: events: %v", err)
		}
	}
}

// eventsList serves GET /v1/events?after=ID&limit=N: merged, oldest first, with
// the unread count and how far the user has seen.
func (s *Server) eventsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	limit := 100
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	f := s.feed()
	f.mu.Lock()
	seen := f.state.Seen
	f.mu.Unlock()
	list, err := f.store.After(after, limit, nil)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	unread, err := f.store.After(seen, 1000, nil)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": list, "unread": len(unread), "seen": seen})
}

// eventsSeen serves POST /v1/events/seen {"id": N}; it never goes backwards.
func (s *Server) eventsSeen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID int64 `json:"id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, http.StatusRequestEntityTooLarge, "too large")
			return
		}
		fail(w, http.StatusBadRequest, `send {"id": 123}`)
		return
	}
	f := s.feed()
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.ID > f.state.Seen {
		f.state.Seen = req.ID
		if err := f.saveLocked(); err != nil {
			fail(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// client talks through the tunnel, with short calls.
func (t *tunnel) client() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, Transport: t.proxy.Transport}
}
