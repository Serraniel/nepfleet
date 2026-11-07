// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/remote"
)

func TestNotifySecurityOnce(t *testing.T) {
	e := &env{fleet: &fleet.Fleet{}, common: common{stateDir: t.TempDir(), host: "h"}}
	problem := remote.Finding{Check: "ports", Level: remote.LevelBad, Message: "port 5432 listens"}
	count := func() int {
		t.Helper()
		got, err := eventStore(e.stateDir).After(0, 50, nil)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	for range 2 {
		if err := e.notifySecurity([]remote.Finding{problem, {Level: remote.LevelWarn, Message: "not a problem"}}, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if count() != 1 {
		t.Errorf("%d events, want 1", count())
	}
	// Fixed, then back.
	if err := e.notifySecurity(nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := e.notifySecurity([]remote.Finding{problem}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if count() != 2 {
		t.Errorf("%d events, want 2", count())
	}
	// Damaged state reports again rather than staying quiet.
	if err := os.WriteFile(filepath.Join(e.stateDir, "security", "problems.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.notifySecurity([]remote.Finding{problem}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if count() != 3 {
		t.Errorf("%d events, want 3", count())
	}
}
