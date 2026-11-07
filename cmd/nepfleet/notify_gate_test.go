// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io"
	"testing"
	"time"

	"github.com/nicholas-fedor/shoutrrr/pkg/types"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/notify"
)

type countingSender struct{ sent []string }

func (s *countingSender) Send(message string, _ *types.Params) []error {
	s.sent = append(s.sent, message)
	return nil
}

func gatedEnv(t *testing.T) (*env, *notify.Notifier, *countingSender) {
	t.Helper()
	n, err := notify.New([]notify.Channel{{Name: "c", URL: "ntfy://ntfy.sh/test"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &countingSender{}
	n.SetSender("c", s)
	return &env{fleet: &fleet.Fleet{}, stateDir: t.TempDir(), host: "h"}, n, s
}

func TestEmitDedups(t *testing.T) {
	e, n, s := gatedEnv(t)
	e.fleet.Spec.Notifications.DedupWindow = "1h"
	ev := notify.Event{Name: "container.crashed", Severity: notify.Warning, Stack: "web", Title: "app crashed"}
	e.emit(n, io.Discard, ev)
	e.emit(n, io.Discard, ev)
	if len(s.sent) != 1 {
		t.Errorf("sent %d times", len(s.sent))
	}
	// The feed still has both.
	got, err := eventStore(e.stateDir).After(0, 10, nil)
	if err != nil || len(got) != 2 {
		t.Errorf("feed: %d %v", len(got), err)
	}
}

func TestEmitQuietHours(t *testing.T) {
	e, n, s := gatedEnv(t)
	now := time.Now().UTC()
	e.fleet.Spec.Notifications.QuietHours = &fleet.QuietHours{
		From: now.Add(-time.Hour).Format("15:04"), To: now.Add(time.Hour).Format("15:04"), Timezone: "UTC",
	}
	e.emit(n, io.Discard, notify.Event{Name: "apply.succeeded", Severity: notify.Info, Stack: "web", Title: "web applied"})
	if len(s.sent) != 0 {
		t.Errorf("an info event went out during quiet hours: %v", s.sent)
	}
	e.emit(n, io.Discard, notify.Event{Name: "apply.failed", Severity: notify.Error, Stack: "web", Title: "web failed"})
	if len(s.sent) != 1 {
		t.Errorf("an error must go out at once: %v", s.sent)
	}
	// Later, the quiet hours are over: the next look sends the digest.
	e.fleet.Spec.Notifications.QuietHours = &fleet.QuietHours{
		From: now.Add(2 * time.Hour).Format("15:04"), To: now.Add(3 * time.Hour).Format("15:04"), Timezone: "UTC",
	}
	e.sendDigests(n, io.Discard)
	if len(s.sent) != 2 {
		t.Errorf("no digest: %v", s.sent)
	}
}
