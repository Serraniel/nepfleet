// SPDX-License-Identifier: AGPL-3.0-or-later

// Package events keeps a host's recent notification events, so clients that
// weren't there can catch up. The CLI, nepfleet watch and the API server are
// separate processes, so a file lock orders them.
package events

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/flock"
)

// defaultKeep is how many events stay.
const defaultKeep = 1000

// Event is one recorded notification.
type Event struct {
	ID       int64     `json:"id"`
	Time     time.Time `json:"time"`
	Name     string    `json:"name"`
	Severity string    `json:"severity"`
	Stack    string    `json:"stack,omitempty"`
	Host     string    `json:"host,omitempty"`
	// Fleet names the remote, in the local control daemon's merged feed.
	Fleet   string `json:"fleet,omitempty"`
	Title   string `json:"title"`
	Message string `json:"message,omitempty"`
}

// Store is <dir>/events.jsonl, owner-only: messages can hold log lines.
type Store struct {
	Dir string
	Now func() time.Time
	// Keep is how many events stay (default 1000). The file is cut back at a
	// fifth more, so most writes only append.
	Keep int
}

func (s *Store) path() string { return filepath.Join(s.Dir, "events.jsonl") }

// Add records an event and gives it the next ID.
func (s *Store) Add(e Event) (Event, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return e, err
	}
	release, err := flock.Lock(filepath.Join(s.Dir, ".lock"), true)
	if err != nil {
		return e, err
	}
	defer release()
	all, err := s.read()
	if err != nil {
		return e, err
	}
	e.ID = 1
	if len(all) > 0 {
		e.ID = all[len(all)-1].ID + 1
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	e.Time = now().UTC().Truncate(time.Millisecond)
	keep := s.Keep
	if keep <= 0 {
		keep = defaultKeep
	}
	if len(all)+1 > keep+keep/5 {
		return e, s.rewrite(append(all[len(all)+1-keep:], e))
	}
	line, err := json.Marshal(e)
	if err != nil {
		return e, err
	}
	f, err := os.OpenFile(s.path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // our own state directory
	if err != nil {
		return e, err
	}
	defer func() { _ = f.Close() }()
	// A leading newline: a torn line from a crash can't swallow this event.
	// read skips the empty lines this leaves.
	_, err = f.Write(append(append([]byte{'\n'}, line...), '\n'))
	return e, err
}

// After returns events newer than id, oldest first, at most limit, with the
// ones allowed; a nil allow takes them all.
func (s *Store) After(id int64, limit int, allow func(Event) bool) ([]Event, error) {
	all, err := s.read()
	if err != nil {
		return nil, err
	}
	out := []Event{}
	for _, e := range all {
		if e.ID <= id || (allow != nil && !allow(e)) {
			continue
		}
		out = append(out, e)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// read tolerates a torn last line from a crash; IDs stay increasing.
func (s *Store) read() ([]Event, error) {
	raw, err := os.ReadFile(s.path())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Event
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID > 0 {
			out = append(out, e)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("events: %w", err)
	}
	return out, nil
}

func (s *Store) rewrite(all []Event) error {
	var b strings.Builder
	for _, e := range all {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	tmp, err := os.CreateTemp(s.Dir, ".events-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(b.String()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path())
}
