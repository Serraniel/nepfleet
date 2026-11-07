// SPDX-License-Identifier: AGPL-3.0-or-later

package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gateFor(t *testing.T, dedup, from, to string) *Gate {
	t.Helper()
	p, err := ParsePolicy(dedup, from, to, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	return &Gate{Dir: t.TempDir(), Policy: p}
}

func at(hour, minute int) time.Time {
	return time.Date(2026, 11, 7, hour, minute, 0, 0, time.UTC)
}

func TestParsePolicy(t *testing.T) {
	for _, tc := range [][4]string{
		{"soon", "", "", ""},
		{"-1h", "", "", ""},
		{"", "22:00", "", ""},
		{"", "25:00", "07:00", ""},
		{"", "22:00", "22:00", ""},
		{"", "22:00", "07:00", "Mars/Olympus"},
	} {
		if _, err := ParsePolicy(tc[0], tc[1], tc[2], tc[3]); err == nil {
			t.Errorf("%v must be refused", tc)
		}
	}
	if p, err := ParsePolicy("", "", "", ""); err != nil || p.Quiet || p.Dedup != 0 {
		t.Errorf("nothing set lets everything through: %+v %v", p, err)
	}
	p, err := ParsePolicy("", "22:00", "07:00", "Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	// 21:30 UTC is 22:30 in Berlin in November.
	if !p.quietAt(at(21, 30)) || p.quietAt(at(20, 30)) {
		t.Error("the time zone isn't used")
	}
}

func TestQuietHoursWindow(t *testing.T) {
	over := gateFor(t, "", "22:00", "07:00").Policy
	day := gateFor(t, "", "12:00", "13:00").Policy
	for _, tc := range []struct {
		p    Policy
		h, m int
		want bool
	}{
		{over, 22, 0, true}, {over, 23, 59, true}, {over, 0, 0, true}, {over, 6, 59, true},
		{over, 7, 0, false}, {over, 21, 59, false},
		{day, 12, 30, true}, {day, 13, 0, false}, {day, 11, 59, false},
	} {
		if got := tc.p.quietAt(at(tc.h, tc.m)); got != tc.want {
			t.Errorf("%02d:%02d: %v", tc.h, tc.m, got)
		}
	}
}

func TestDedup(t *testing.T) {
	g := gateFor(t, "1h", "", "")
	e := Event{Name: "container.crashed", Severity: Warning, Stack: "web", Title: "app crashed"}
	check := func(e Event, now time.Time, want Action) {
		t.Helper()
		got, err := g.Admit(e, []string{"c"}, now)
		if err != nil || got != want {
			t.Errorf("%v (%v), want %v", got, err, want)
		}
	}
	check(e, at(10, 0), Deliver)
	// A failed delivery isn't noted, so the next try still goes out.
	check(e, at(10, 1), Deliver)
	if err := g.Delivered(e, at(10, 1)); err != nil {
		t.Fatal(err)
	}
	check(e, at(10, 30), Duplicate)
	other := e
	other.Stack = "db"
	check(other, at(10, 30), Deliver)
	other = e
	other.Title = "worker crashed"
	check(other, at(10, 30), Deliver)
	check(e, at(11, 1), Deliver)
}

func TestQuietHoldsInfoOnly(t *testing.T) {
	g := gateFor(t, "1h", "22:00", "07:00")
	info := Event{Name: "apply.succeeded", Severity: Info, Stack: "web", Title: "web applied"}
	if got, _ := g.Admit(info, []string{"a", "b"}, at(23, 0)); got != Held {
		t.Errorf("info: %v", got)
	}
	// A repeat within the window isn't held twice.
	if got, _ := g.Admit(info, []string{"a", "b"}, at(23, 5)); got != Duplicate {
		t.Errorf("repeat: %v", got)
	}
	for _, sev := range []string{Warning, Error} {
		e := Event{Name: "x", Severity: sev, Title: sev}
		if got, _ := g.Admit(e, []string{"a"}, at(23, 0)); got != Deliver {
			t.Errorf("%s must not wait: %v", sev, got)
		}
	}
	if due, err := g.Due(at(23, 30)); err != nil || len(due) != 0 {
		t.Errorf("nothing is due during quiet hours: %v %v", due, err)
	}
	due, err := g.Due(at(7, 0))
	if err != nil || len(due) != 2 || due[0].Channel != "a" || due[1].Channel != "b" {
		t.Fatalf("%+v %v", due, err)
	}
	if !strings.Contains(due[0].Event.Message, "web applied (web)") || !strings.HasPrefix(due[0].Event.Title, "1 events") {
		t.Errorf("%+v", due[0].Event)
	}
	if due, _ := g.Due(at(7, 1)); len(due) != 0 {
		t.Errorf("sent twice: %+v", due)
	}
}

func TestDigestIsBounded(t *testing.T) {
	g := gateFor(t, "", "22:00", "07:00")
	for i := range maxHeld + 10 {
		e := Event{Name: "x", Severity: Info, Title: "event " + time.Duration(i).String()}
		if got, _ := g.Admit(e, []string{"a"}, at(23, 0)); got != Held {
			t.Fatal(got)
		}
	}
	due, err := g.Due(at(8, 0))
	if err != nil || len(due) != 1 {
		t.Fatalf("%v %v", due, err)
	}
	if lines := strings.Count(due[0].Event.Message, "\n") + 1; lines != maxDigestLines+1 {
		t.Errorf("%d lines", lines)
	}
	if !strings.HasPrefix(due[0].Event.Title, "210 events") {
		t.Errorf("%s", due[0].Event.Title)
	}
	if !strings.Contains(due[0].Event.Message, "and 180 more") {
		t.Errorf("%s", due[0].Event.Message)
	}
}

func TestDamagedStateLetsEventsThrough(t *testing.T) {
	g := gateFor(t, "1h", "", "")
	if err := os.WriteFile(filepath.Join(g.Dir, "state.json"), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := Event{Name: "x", Severity: Error, Title: "y"}
	if got, err := g.Admit(e, nil, at(10, 0)); err != nil || got != Deliver {
		t.Errorf("%v %v", got, err)
	}
	// With no way to read it at all, too.
	broken := &Gate{Dir: filepath.Join(g.Dir, "state.json", "below"), Policy: g.Policy}
	if got, err := broken.Admit(e, nil, at(10, 0)); err == nil || got != Deliver {
		t.Errorf("%v %v", got, err)
	}
}
