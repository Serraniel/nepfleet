// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"errors"
	"testing"
	"time"
)

type pullRunner struct {
	errs  []error
	calls int
}

func (r *pullRunner) Run(context.Context, string, ...string) ([]byte, error) {
	r.calls++
	if len(r.errs) == 0 {
		return nil, nil
	}
	err := r.errs[0]
	r.errs = r.errs[1:]
	return nil, err
}

func fastPullWaits(t *testing.T) {
	t.Helper()
	old := pullWaits
	pullWaits = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { pullWaits = old })
}

func TestPullImage(t *testing.T) {
	fastPullWaits(t)
	limited := errors.New("docker pull: error from registry: retry-after: 1.06ms, allowed: 44000/minute")
	ctx := context.Background()

	r := &pullRunner{errs: []error{limited, limited}}
	if err := pullImage(ctx, r, "x"); err != nil || r.calls != 3 {
		t.Errorf("recovers: %v, %d calls", err, r.calls)
	}
	r = &pullRunner{errs: []error{limited, limited, limited, limited}}
	if err := pullImage(ctx, r, "x"); !errors.Is(err, limited) || r.calls != 3 {
		t.Errorf("gives up: %v, %d calls", err, r.calls)
	}
	// Any other failure won't get better by waiting.
	other := errors.New("manifest unknown")
	r = &pullRunner{errs: []error{other}}
	if err := pullImage(ctx, r, "x"); !errors.Is(err, other) || r.calls != 1 {
		t.Errorf("other errors: %v, %d calls", err, r.calls)
	}
	r = &pullRunner{}
	if err := pullImage(ctx, r, "x"); err != nil || r.calls != 1 {
		t.Errorf("no trouble: %v, %d calls", err, r.calls)
	}
}

func TestPullImageStopsWhenCancelled(t *testing.T) {
	old := pullWaits
	pullWaits = []time.Duration{time.Hour}
	t.Cleanup(func() { pullWaits = old })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &pullRunner{errs: []error{errors.New("429")}}
	done := make(chan error, 1)
	go func() { done <- pullImage(ctx, r, "x") }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("cancelled, but no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting after being cancelled")
	}
}

func TestRateLimited(t *testing.T) {
	for msg, want := range map[string]bool{
		"toomanyrequests: slow down": true,
		"HTTP 429":                   true,
		"Too Many Requests":          true,
		"manifest unknown":           false,
	} {
		if rateLimited(errors.New(msg)) != want {
			t.Errorf("%s", msg)
		}
	}
	if rateLimited(nil) {
		t.Error("no error")
	}
}
