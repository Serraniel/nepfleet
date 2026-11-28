// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/control"
)

// exeWatchEnv sets how often the daemon looks at its binary (tests shorten it).
const exeWatchEnv = "NEPFLEET_CONTROL_WATCH_EVERY"

// watchExecutable follows updates of the daemon's own binary: once the file
// has changed, holds still, and says it is another build, restart (a handover,
// see execHandoff) runs it. While a setup runs, it tries again later.
func watchExecutable(ctx context.Context, exe string, every time.Duration, restart func() error) {
	base, err := os.Stat(exe)
	if err != nil {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	var pending os.FileInfo
	waiting := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		fi, err := os.Stat(exe)
		if err != nil {
			continue // between the package manager's remove and rename
		}
		if sameFile(fi, base) {
			pending = nil
			continue
		}
		// Written over several looks: wait until it holds still.
		if pending == nil || !sameFile(fi, pending) {
			pending = fi
			continue
		}
		if !otherBuild(ctx, exe) {
			base, pending = fi, nil
			continue
		}
		err = restart()
		if err == nil {
			return
		}
		if !waiting {
			waiting = true
			log.Printf("control: nepfleet was updated; this daemon restarts into it once nothing runs: %v", err)
		}
		if !errors.Is(err, control.ErrBusy) {
			return
		}
	}
}

func sameFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// otherBuild tells whether exe is another build than this one. One that
// doesn't run isn't restarted into.
func otherBuild(ctx context.Context, exe string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "version").Output() //nolint:gosec // this program's own file
	if err != nil {
		return false
	}
	v, c, ok := parseVersion(string(out))
	return ok && (v != version || c != commit)
}

// parseVersion reads `nepfleet version`: "nepfleet 0.4.0 (abc123)".
func parseVersion(out string) (ver, com string, ok bool) {
	f := strings.Fields(out)
	if len(f) != 3 || f[0] != "nepfleet" || !strings.HasPrefix(f[2], "(") || !strings.HasSuffix(f[2], ")") {
		return "", "", false
	}
	return f[1], strings.Trim(f[2], "()"), true
}

// watchEvery is how often the daemon looks at its binary.
func watchEvery() time.Duration {
	if d, err := time.ParseDuration(os.Getenv(exeWatchEnv)); err == nil && d > 0 {
		return d
	}
	return 10 * time.Second
}
