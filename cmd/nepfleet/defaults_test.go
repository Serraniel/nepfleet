// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/api"
)

// The apply: manual NepFleet wrote at creation becomes the default; one a
// person set stays.
func TestAdoptDefaults(t *testing.T) {
	c, dir := newStackFleet(t)
	// Each created as the old create did: apply: manual in its new-stack
	// commit (amended, as that commit is the newest each time).
	for _, name := range []string{"written", "chosen"} {
		if _, err := newStackAPI(c, api.NewStack{Template: "hello", Source: "local", Name: name, Values: map[string]string{"GREETING": "hi"}}, "Ada"); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "stacks", name, "stack.yaml")
		raw, err := os.ReadFile(p) //nolint:gosec // the test's fleet
		if err != nil {
			t.Fatal(err)
		}
		out := strings.Replace(string(raw), "apply: auto", "apply: manual", 1)
		if out == string(raw) {
			t.Fatalf("no apply line to change:\n%s", raw)
		}
		if err := os.WriteFile(p, []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
		tgit(t, dir, "commit", "-qa", "--amend", "--no-edit", "--no-verify")
	}
	// A person sets chosen to manual again later: their choice.
	p := filepath.Join(dir, "stacks", "chosen", "stack.yaml")
	raw, _ := os.ReadFile(p) //nolint:gosec // the test's fleet
	if err := os.WriteFile(p, []byte(strings.Replace(string(raw), "apply: manual", "apply: auto", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "commit", "-qam", "auto for a while", "--no-verify")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "commit", "-qam", "review chosen by hand", "--no-verify")

	e, err := c.open()
	if err != nil {
		t.Fatal(err)
	}
	if moved := adoptDefaults(e, io.Discard); !slices.Equal(moved, []string{"written"}) {
		t.Fatalf("moved %v", moved)
	}
	w, _ := os.ReadFile(filepath.Join(dir, "stacks", "written", "stack.yaml")) //nolint:gosec // the test's fleet
	ch, _ := os.ReadFile(p)                                                    //nolint:gosec // the test's fleet
	if strings.Contains(string(w), "apply:") || !strings.Contains(string(ch), "apply: manual") {
		t.Errorf("written:\n%s\nchosen:\n%s", w, ch)
	}
	if got := tgit(t, dir, "log", "-1", "--format=%an|%s"); got != "nepfleet|chore(written): apply changes by itself, as new stacks do" {
		t.Errorf("commit: %s", got)
	}
	if s := tgit(t, dir, "status", "--porcelain"); s != "" {
		t.Errorf("dirty: %s", s)
	}
	// Nothing more the second time.
	if moved := adoptDefaults(e, io.Discard); len(moved) != 0 {
		t.Errorf("again: %v", moved)
	}
}
