// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/control"
)

// exeWatchEnv sets how often the daemon looks at its binary (tests shorten it).
const exeWatchEnv = "NEPFLEET_CONTROL_WATCH_EVERY"

// handoffLevel is what this binary understands of a handover (handoffEnv):
// a daemon execs only into a binary that says 1 or more. A var for tests.
var handoffLevel = 1

// watchExecutable follows updates of the daemon's own binary: once the file
// has changed, holds still, and is another build that can take over in place,
// restart (a handover, see execHandoff) runs it. While something runs, it
// tries again later. A build that can't take over is left alone: the app says
// which package to update, and a restart from it stops and starts.
func watchExecutable(ctx context.Context, exe string, every time.Duration, restart func() error) {
	base, err := os.Stat(exe)
	if err != nil {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	var pending os.FileInfo
	told := ""
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
		b, err := probeBuild(ctx, exe)
		if err != nil {
			continue // half installed, or busy: look again
		}
		if !b.other() {
			base, pending = fi, nil
			continue
		}
		if b.handoff < 1 {
			if told != b.version {
				told = b.version
				log.Printf("control: nepfleet %s is installed but can't take over in place; restart the daemon to run it", b.version)
			}
			base, pending = fi, nil
			continue
		}
		err = restart()
		if err == nil {
			return
		}
		if told != "busy" {
			told = "busy"
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

// build is what `nepfleet version --handoff` says about a binary.
type build struct {
	version, commit string
	handoff         int
}

// other tells whether it is another build than this one.
func (b build) other() bool { return b.version != version || b.commit != commit }

// probeBuild runs exe's `version --handoff`. An error means it doesn't run.
func probeBuild(ctx context.Context, exe string) (build, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "version", "--handoff").Output() //nolint:gosec // this program's own file
	if err != nil {
		return build{}, err
	}
	b, ok := parseVersion(string(out))
	if !ok {
		return build{}, fmt.Errorf("%s version said %q", exe, strings.TrimSpace(string(out)))
	}
	return b, nil
}

// parseVersion reads `nepfleet version`: "nepfleet 0.4.0 (abc123)", with
// --handoff from binaries that know it " handoff 1".
func parseVersion(out string) (build, bool) {
	f := strings.Fields(out)
	if len(f) < 3 || f[0] != "nepfleet" || !strings.HasPrefix(f[2], "(") || !strings.HasSuffix(f[2], ")") {
		return build{}, false
	}
	b := build{version: f[1], commit: strings.Trim(f[2], "()")}
	if len(f) == 5 && f[3] == "handoff" {
		b.handoff, _ = strconv.Atoi(f[4])
	}
	return b, true
}

// watchEvery is how often the daemon looks at its binary.
func watchEvery() time.Duration {
	if d, err := time.ParseDuration(os.Getenv(exeWatchEnv)); err == nil && d > 0 {
		return d
	}
	return 10 * time.Second
}
