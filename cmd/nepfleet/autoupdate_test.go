// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

func TestDecideUpdate(t *testing.T) {
	rebuilt := []imageUpdate{{Image: "a", Current: "1", Rebuilt: true}}
	newer := []imageUpdate{{Image: "a", Current: "1", Next: "2"}}
	both := append(append([]imageUpdate{}, rebuilt...), newer...)
	beyond := []imageUpdate{{Image: "a", Current: "1", Beyond: "9"}}
	for _, tc := range []struct {
		name    string
		mode    string
		updates []imageUpdate
		window  bool
		dirty   bool
		want    updateAction
	}{
		{"off", "off", both, true, false, updateNothing},
		{"current", "auto", nil, true, false, updateNothing},
		{"beyond the track is information only", "auto", beyond, true, false, updateNothing},
		{"notify never applies", "notify", rebuilt, true, false, updateNotify},
		{"a rebuilt tag applies", "auto", rebuilt, true, false, updateApply},
		{"a new tag needs a template update", "auto", newer, true, false, updateNotify},
		{"both: the rebuild applies", "auto", both, true, false, updateApply},
		{"outside the window", "auto", rebuilt, false, false, updateWait},
		{"uncommitted changes", "auto", rebuilt, true, true, updateWait},
	} {
		if got := decideUpdate(tc.mode, tc.updates, tc.window, tc.dirty); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNotifiedOnce(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		stack, summary string
		want           bool
	}{
		{"a", "x", true}, {"a", "x", false}, {"b", "x", true}, {"a", "y", true}, {"a", "y", false},
	} {
		got, err := notifiedOnce(dir, tc.stack, tc.summary)
		if err != nil || got != tc.want {
			t.Errorf("%+v: %v %v", tc, got, err)
		}
	}
}

func TestUpdateSummary(t *testing.T) {
	got := updateSummary([]imageUpdate{{Image: "b", Current: "1", Next: "2"}, {Image: "a", Current: "3", Rebuilt: true}, {Image: "c"}})
	if got != "a:3 was rebuilt, b 1 → 2" {
		t.Error(got)
	}
}

func TestUpdateWindow(t *testing.T) {
	e := &env{fleet: &fleet.Fleet{}}
	now := time.Date(2026, 11, 7, 3, 30, 0, 0, time.UTC)
	if ok, err := e.inUpdateWindow(now); !ok || err != nil {
		t.Errorf("no window means any time: %v %v", ok, err)
	}
	e.fleet.Spec.Updates.Window = &fleet.QuietHours{From: "02:00", To: "05:00", Timezone: "UTC"}
	if ok, err := e.inUpdateWindow(now); !ok || err != nil {
		t.Errorf("inside: %v %v", ok, err)
	}
	if ok, err := e.inUpdateWindow(now.Add(6 * time.Hour)); ok || err != nil {
		t.Errorf("outside: %v %v", ok, err)
	}
}
