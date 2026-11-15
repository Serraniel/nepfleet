// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestParseHostUpdates(t *testing.T) {
	h := parseHostUpdates([]byte("os=arch\npkg=linux\npkg=openssl\npkg=bad name; rm\nsecurity=1\nreboot=yes\nnote=arch-audit\x1b[31m output\nnoise\n"))
	if h.OS != "arch" || !h.Supported || h.Pending != 2 || h.Security != 1 || h.RebootRequired == nil || !*h.RebootRequired {
		t.Fatalf("%+v", h)
	}
	if strings.Join(h.Packages, ",") != "linux,openssl" {
		t.Errorf("packages %v", h.Packages)
	}
	if len(h.Notes) != 1 || strings.ContainsRune(h.Notes[0], '\x1b') {
		t.Errorf("notes %q", h.Notes)
	}
	// Unknown stays unknown: no arch-audit, no reboot answer.
	u := parseHostUpdates([]byte("os=fedora\nreboot=unknown\n"))
	if u.Security != -1 || u.RebootRequired != nil || u.Pending != 0 {
		t.Errorf("unknowns: %+v", u)
	}
	if n := parseHostUpdates([]byte("os=gentoo\nunsupported=yes\n")); n.Supported || n.OS != "gentoo" {
		t.Errorf("unsupported: %+v", n)
	}
	if o := parseHostUpdates([]byte("os=../etc\n")); o.OS != "unknown" {
		t.Errorf("odd os id kept: %q", o.OS)
	}
}

// The script runs with a POSIX sh, whatever the distribution (here: this
// machine), and reports at least the OS.
func TestHostUpdatesScriptRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("asks this machine's package manager, which may sync from the network")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	run := func(ctx context.Context, _ Target, script string) ([]byte, error) {
		return exec.CommandContext(ctx, "sh", "-c", script).Output()
	}
	h, err := CheckHostUpdates(context.Background(), run, Target{})
	if err != nil {
		t.Fatal(err)
	}
	if h.OS == "" || h.Pending < 0 {
		t.Errorf("%+v", h)
	}
	t.Logf("this machine: %+v", h)
}
