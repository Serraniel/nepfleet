// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/Serraniel/nepfleet/internal/flock"
)

// Delivery is a channel's last send attempt, for the app to show whether a
// channel works. Error never holds a URL: those carry tokens.
type Delivery struct {
	// At is the last attempt; OK whether it went out.
	At    time.Time `json:"at"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	// Delivered is the last attempt that went out, if any.
	Delivered *time.Time `json:"delivered,omitempty"`
}

// maxDeliveryError keeps a stored reason short.
const maxDeliveryError = 300

// anyURL matches URLs in an error, which for channels carry tokens.
var anyURL = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"']+`)

// Deliveries keeps each channel's last delivery in <dir>/deliveries.json.
// Every apply and timer is its own process, so the file is the record. The
// last state seen is also kept in memory, for when the file can't be read.
type Deliveries struct {
	Dir string

	mu   sync.Mutex
	last map[string]Delivery
}

func (d *Deliveries) path() string { return filepath.Join(d.Dir, "deliveries.json") }

// Record notes an attempt to a channel; err nil means it went out.
func (d *Deliveries) Record(channel string, err error, now time.Time) error {
	if d == nil {
		return nil
	}
	rec := Delivery{At: now.UTC(), OK: err == nil}
	if err != nil {
		reason := anyURL.ReplaceAllString(err.Error(), "<url>")
		if r := []rune(reason); len(r) > maxDeliveryError {
			reason = string(r[:maxDeliveryError]) + "…"
		}
		rec.Error = reason
	} else {
		t := rec.At
		rec.Delivered = &t
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(d.Dir, 0o700); err != nil {
		return err
	}
	unlock, err := flock.Lock(filepath.Join(d.Dir, ".deliveries.lock"), true)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := d.read()
	if err != nil {
		return err
	}
	if rec.Delivered == nil {
		rec.Delivered = s[channel].Delivered
	}
	s[channel] = rec
	d.last = s
	out, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := d.path() + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, d.path())
}

// All returns each channel's last delivery; channels never sent to are
// missing.
func (d *Deliveries) All() (map[string]Delivery, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, err := d.read()
	if err != nil {
		if d.last != nil {
			return copyDeliveries(d.last), nil
		}
		return nil, err
	}
	d.last = s
	return copyDeliveries(s), nil
}

// read loads the file; writes rename a whole file into place, so reading
// needs no lock.
func (d *Deliveries) read() (map[string]Delivery, error) {
	s := map[string]Delivery{}
	raw, err := os.ReadFile(d.path()) //nolint:gosec // the installation's own state directory
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	if json.Unmarshal(raw, &s) != nil || s == nil {
		return map[string]Delivery{}, nil // damaged: start over
	}
	return s, nil
}

func copyDeliveries(s map[string]Delivery) map[string]Delivery {
	out := make(map[string]Delivery, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}
