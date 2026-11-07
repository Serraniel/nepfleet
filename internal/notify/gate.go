// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // named time zones on hosts and images without them

	"github.com/Serraniel/nepfleet/internal/flock"
)

// Policy limits how often people hear about things. The zero value lets
// everything through.
type Policy struct {
	// Dedup sends the same event at most once per this long.
	Dedup time.Duration
	// Quiet holds info events between these minutes of the day, for a digest.
	Quiet    bool
	from, to int
	loc      *time.Location
}

func minuteOfDay(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a time like 22:00", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// ParsePolicy reads fleet.yaml's settings. An empty timezone means the host's.
func ParsePolicy(dedup, from, to, timezone string) (Policy, error) {
	p := Policy{loc: time.Local}
	if dedup != "" {
		d, err := time.ParseDuration(dedup)
		if err != nil || d <= 0 {
			return p, fmt.Errorf("dedupWindow: %q is not a duration like 1h", dedup)
		}
		p.Dedup = d
	}
	if from == "" && to == "" {
		return p, nil
	}
	var err error
	if p.from, err = minuteOfDay(from); err != nil {
		return p, fmt.Errorf("quietHours.from: %w", err)
	}
	if p.to, err = minuteOfDay(to); err != nil {
		return p, fmt.Errorf("quietHours.to: %w", err)
	}
	if p.from == p.to {
		return p, errors.New("quietHours: from and to are the same")
	}
	if timezone != "" {
		if p.loc, err = time.LoadLocation(timezone); err != nil {
			return p, fmt.Errorf("quietHours.timezone: %q is unknown", timezone)
		}
	}
	p.Quiet = true
	return p, nil
}

func (p Policy) quietAt(t time.Time) bool {
	if !p.Quiet {
		return false
	}
	t = t.In(p.loc)
	m := t.Hour()*60 + t.Minute()
	if p.from < p.to {
		return m >= p.from && m < p.to
	}
	return m >= p.from || m < p.to // over midnight
}

// Action is what to do with an event.
type Action int

// What Gate.Admit decides.
const (
	Deliver Action = iota
	Duplicate
	Held
)

const (
	maxHeld        = 200
	maxDigestLines = 30
)

type heldEvent struct {
	Channels []string `json:"channels"`
	Title    string   `json:"title"`
	Stack    string   `json:"stack,omitempty"`
}

type gateState struct {
	Sent map[string]time.Time `json:"sent"`
	Held []heldEvent          `json:"held,omitempty"`
	// Dropped counts held events beyond maxHeld.
	Dropped int `json:"dropped,omitempty"`
}

// Gate applies a policy across processes: every apply is its own, so what was
// sent is kept in <dir>/state.json. If that can't be read, events go out: too
// many messages beat a missed one.
type Gate struct {
	Dir    string
	Policy Policy
}

func (g *Gate) with(change func(*gateState) bool) error {
	if err := os.MkdirAll(g.Dir, 0o700); err != nil {
		return err
	}
	unlock, err := flock.Lock(filepath.Join(g.Dir, ".state.lock"), true)
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(g.Dir, "state.json")
	var s gateState
	raw, err := os.ReadFile(path) //nolint:gosec // the installation's own state directory
	if err == nil {
		if json.Unmarshal(raw, &s) != nil {
			s = gateState{} // damaged: start over
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if s.Sent == nil {
		s.Sent = map[string]time.Time{}
	}
	if !change(&s) {
		return nil
	}
	out, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func gateKey(e Event) string {
	return strings.Join([]string{e.Name, e.Stack, e.Host, e.Title}, "\x00")
}

// Admit decides about an event going to channels. Warnings and errors are never
// held. An error from it comes with Deliver.
func (g *Gate) Admit(e Event, channels []string, now time.Time) (Action, error) {
	action := Deliver
	err := g.with(func(s *gateState) bool {
		key := gateKey(e)
		if g.Policy.Dedup > 0 {
			if last, ok := s.Sent[key]; ok && now.Sub(last) < g.Policy.Dedup {
				action = Duplicate
				return false
			}
		}
		if e.Severity != Info && e.Severity != "" || !g.Policy.quietAt(now) {
			return false
		}
		action = Held
		if len(s.Held) >= maxHeld {
			s.Dropped++
		} else {
			s.Held = append(s.Held, heldEvent{Channels: channels, Title: e.Title, Stack: e.Stack})
		}
		if g.Policy.Dedup > 0 {
			s.Sent[key] = now // a repeat during quiet hours is the same event
		}
		return true
	})
	if err != nil {
		return Deliver, err
	}
	return action, nil
}

// Delivered notes an event that went out, so repeats can be recognised.
func (g *Gate) Delivered(e Event, now time.Time) error {
	if g.Policy.Dedup <= 0 {
		return nil
	}
	return g.with(func(s *gateState) bool {
		for k, t := range s.Sent {
			if now.Sub(t) >= g.Policy.Dedup {
				delete(s.Sent, k)
			}
		}
		s.Sent[gateKey(e)] = now
		return true
	})
}

// Digest is what a channel missed during quiet hours.
type Digest struct {
	Channel string
	Event   Event
}

// Due returns the digests to send once quiet hours are over, and forgets them.
func (g *Gate) Due(now time.Time) ([]Digest, error) {
	if g.Policy.quietAt(now) {
		return nil, nil
	}
	var out []Digest
	err := g.with(func(s *gateState) bool {
		if len(s.Held) == 0 {
			return false
		}
		byChannel := map[string][]heldEvent{}
		for _, h := range s.Held {
			for _, c := range h.Channels {
				byChannel[c] = append(byChannel[c], h)
			}
		}
		channels := make([]string, 0, len(byChannel))
		for c := range byChannel {
			channels = append(channels, c)
		}
		slices.Sort(channels)
		for _, c := range channels {
			out = append(out, Digest{Channel: c, Event: digestEvent(byChannel[c], s.Dropped)})
		}
		s.Held, s.Dropped = nil, 0
		return true
	})
	return out, err
}

func digestEvent(held []heldEvent, dropped int) Event {
	var b strings.Builder
	for i, h := range held {
		if i == maxDigestLines {
			break
		}
		b.WriteString("- " + h.Title)
		if h.Stack != "" {
			b.WriteString(" (" + h.Stack + ")")
		}
		b.WriteString("\n")
	}
	if more := max(len(held)-maxDigestLines, 0) + dropped; more > 0 {
		fmt.Fprintf(&b, "and %d more\n", more)
	}
	return Event{
		Name: "notify.digest", Severity: Info,
		Title:   fmt.Sprintf("%d events during quiet hours", len(held)+dropped),
		Message: strings.TrimRight(b.String(), "\n"),
	}
}
