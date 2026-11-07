// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

// termenv and Bubble Tea v1 query the terminal when a program starts (their
// package init), so every nepfleet command would, even without any UI: it can
// stall for seconds and eat typed input.
func TestNoTerminalQueryAtStartup(t *testing.T) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build info")
	}
	for _, d := range info.Deps {
		if d.Path == "github.com/muesli/termenv" || strings.HasPrefix(d.Path, "github.com/charmbracelet/bubbletea") && !strings.Contains(d.Path, "/v2") {
			t.Errorf("%s queries the terminal at startup", d.Path)
		}
	}
}
