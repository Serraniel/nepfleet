// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"
	"time"
)

func ev(action, id string, attrs map[string]string) dockerEvent {
	var e dockerEvent
	e.Action = action
	e.Actor.ID = id
	e.Actor.Attributes = map[string]string{"name": "web-1", "com.docker.compose.project": "web"}
	for k, v := range attrs {
		e.Actor.Attributes[k] = v
	}
	return e
}

func TestWatcher(t *testing.T) {
	w := newWatcher(func(string) string { return "panic: boom" })
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	got := w.process(ev("die", "a", map[string]string{"exitCode": "1"}), now)
	if len(got) != 1 || got[0].Name != "container.crashed" || got[0].Stack != "web" || !strings.Contains(got[0].Message, "panic: boom") {
		t.Fatalf("crash: %+v", got)
	}
	// A second crash right away is not repeated; the third within the window is a loop.
	if got := w.process(ev("die", "a", map[string]string{"exitCode": "1"}), now.Add(time.Minute)); len(got) != 0 {
		t.Errorf("repeated: %+v", got)
	}
	if got := w.process(ev("die", "a", map[string]string{"exitCode": "1"}), now.Add(2*time.Minute)); len(got) != 1 || got[0].Name != "container.crash_loop" {
		t.Errorf("loop: %+v", got)
	}
	// Stops on purpose are not crashes.
	w.process(ev("kill", "b", nil), now)
	if got := w.process(ev("die", "b", map[string]string{"exitCode": "137"}), now.Add(5*time.Second)); len(got) != 0 {
		t.Errorf("a killed container: %+v", got)
	}
	if got := w.process(ev("die", "c", map[string]string{"exitCode": "0"}), now); len(got) != 0 {
		t.Errorf("a clean exit: %+v", got)
	}
	// OOM is reported once, the die that follows is not.
	if got := w.process(ev("oom", "d", nil), now); len(got) != 1 || got[0].Name != "container.oom" {
		t.Errorf("oom: %+v", got)
	}
	if got := w.process(ev("die", "d", map[string]string{"exitCode": "137"}), now.Add(time.Second)); len(got) != 0 {
		t.Errorf("die after oom: %+v", got)
	}
	if got := w.process(ev("health_status: unhealthy", "e", nil), now); len(got) != 1 || got[0].Name != "container.unhealthy" {
		t.Errorf("unhealthy: %+v", got)
	}
}
