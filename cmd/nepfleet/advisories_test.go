// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/sbom"
)

func TestNotifyAdvisoriesOnce(t *testing.T) {
	e := &env{fleet: &fleet.Fleet{}, common: common{stateDir: t.TempDir(), host: "h"}}
	count := func() int {
		t.Helper()
		got, err := eventStore(e.stateDir).After(0, 50, nil)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	one := sbom.Advisory{Image: "a:1", ID: "GHSA-1", Package: "pkg:npm/x@1", Fixed: "2"}
	two := sbom.Advisory{Image: "a:1", ID: "GHSA-2", Package: "pkg:npm/y@1"}
	other := sbom.Advisory{Image: "b:1", ID: "GHSA-1", Package: "pkg:npm/x@1"}
	if err := e.notifyAdvisories([]sbom.Advisory{one, two, other}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if count() != 2 {
		t.Errorf("one event per image: %d", count())
	}
	if err := e.notifyAdvisories([]sbom.Advisory{one, two, other}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if count() != 2 {
		t.Errorf("a repeat: %d", count())
	}
	// Gone, then back: it counts as new.
	if err := e.notifyAdvisories(nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := e.notifyAdvisories([]sbom.Advisory{one}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if count() != 3 {
		t.Errorf("back again: %d", count())
	}
}

func TestNotifyAdvisoriesAreBounded(t *testing.T) {
	e := &env{fleet: &fleet.Fleet{}, common: common{stateDir: t.TempDir(), host: "h"}}
	var many []sbom.Advisory
	for i := range 50 {
		many = append(many, sbom.Advisory{Image: "a:1", ID: fmt.Sprintf("X-%d", i), Package: "p"})
	}
	if err := e.notifyAdvisories(many, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := eventStore(e.stateDir).After(0, 50, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if !strings.Contains(got[0].Message, "and 30 more") || !strings.Contains(got[0].Title, "50 new") {
		t.Errorf("%+v", got[0])
	}
}
