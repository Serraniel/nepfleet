// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/Serraniel/nepfleet/internal/control"
)

// controlInfo is the daemon's GET /v1/info.
type controlInfo struct {
	Version string    `json:"version"`
	Commit  string    `json:"commit"`
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	// HandoffError is why the last restart in place failed (it carries on).
	HandoffError string `json:"handoffError"`
}

// controlError is an error answer of the daemon.
type controlError struct {
	Status  int
	Message string
}

func (e *controlError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("the control daemon answered %d %s", e.Status, http.StatusText(e.Status))
}

func printControlInfo(w io.Writer, info controlInfo) {
	v := info.Version
	if info.Commit != "" {
		v += " (" + info.Commit + ")"
	}
	fmt.Fprintf(w, "running: version %s, pid %d, since %s\n", v, info.PID, info.Started.Local().Format(time.RFC1123))
}

// cmdControlRestart: nepfleet control restart — stops the running daemon and
// starts the binary installed now, after nepfleet was updated. Tunnels and the
// app's port come back by themselves; a running setup makes it refuse.
func cmdControlRestart(stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Held from the stop to the start: the app, finding the daemon gone, asks
	// for a start meanwhile; that waits here and then finds the new daemon.
	unlock, err := lockControlStart(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	var old controlInfo
	if controlCall(ctx, http.MethodGet, "/v1/info", nil, &old) == nil {
		inPlace, err := restartInPlace(ctx, old)
		if err != nil {
			return err
		}
		// Not in place: stop it, if it still runs (a failed handover carries
		// on), and start the one installed now.
		var still controlInfo
		if !inPlace && controlCall(ctx, http.MethodGet, "/v1/info", nil, &still) == nil {
			if err := stopControl(ctx, still); err != nil {
				return err
			}
		}
	}
	if err := startControlLocked(ctx); err != nil {
		return err
	}
	var info controlInfo
	if err := controlCall(ctx, http.MethodGet, "/v1/info", nil, &info); err != nil {
		return err
	}
	printControlInfo(stdout, info)
	return nil
}

// restartInPlace asks the daemon to hand over to the binary installed now: its
// socket and port stay open, and unlocked keys stay unlocked. False when it
// can't (older daemons, Windows, a binary that can't take over), when the
// handover failed and the daemon carries on, or when the new program ended:
// then a normal stop and start follows.
func restartInPlace(ctx context.Context, old controlInfo) (bool, error) {
	err := controlCall(ctx, http.MethodPost, "/v1/restart", nil, nil)
	var ce *controlError
	if errors.As(err, &ce) && (ce.Status == http.StatusNotFound || ce.Status == http.StatusNotImplemented) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Requests wait in the socket's backlog meanwhile: the answer comes from
	// the new program, with a new start time.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var info controlInfo
		err := controlCall(ctx, http.MethodGet, "/v1/info", nil, &info)
		switch {
		case err == nil && info.Started.After(old.Started):
			return true, nil
		case err == nil && info.HandoffError != "":
			return false, nil
		case err != nil && processGone(old.PID):
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false, nil
}

// stopControl stops the daemon and waits until it's gone. A daemon from before
// POST /v1/stop gets SIGTERM instead, once no setup runs on it.
func stopControl(ctx context.Context, info controlInfo) error {
	err := controlCall(ctx, http.MethodPost, "/v1/stop", nil, nil)
	var ce *controlError
	switch {
	case errors.As(err, &ce) && ce.Status == http.StatusNotFound:
		if err := checkNoSetup(ctx); err != nil {
			return err
		}
		p, err := os.FindProcess(info.PID)
		if err != nil {
			return err
		}
		if err := p.Signal(syscall.SIGTERM); err != nil {
			return fmt.Errorf("stopping the control daemon (pid %d): %w", info.PID, err)
		}
	case err != nil:
		return err
	}
	return waitControlGone(ctx, info.PID)
}

// waitControlGone waits, bounded, until no daemon is left: the socket doesn't
// answer, the old process has exited, and nobody holds the daemon lock. The
// socket closes before the daemon has let go of its port and tunnels; the lock
// goes only with the process (daemons from before the lock: the pid tells).
func waitControlGone(ctx context.Context, pid int) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		if controlCall(ctx, http.MethodGet, "/v1/info", nil, nil) != nil && (pid == 0 || processGone(pid)) && !controlLockHeld() {
			return nil
		}
		if time.Now().After(deadline) {
			if pid != 0 {
				return fmt.Errorf("the control daemon (pid %d) did not stop", pid)
			}
			return errors.New("the control daemon did not stop; its socket closed but it still runs")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// checkNoSetup asks an older daemon whether a setup runs on any remote.
func checkNoSetup(ctx context.Context) error {
	var all []control.Remote
	if err := controlCall(ctx, http.MethodGet, "/v1/remotes", nil, &all); err != nil {
		return err
	}
	var busy []string
	for _, r := range all {
		var job control.SetupJob
		if controlCall(ctx, http.MethodGet, "/v1/remotes/"+url.PathEscape(r.Name)+"/setup/job", nil, &job) == nil && job.State == "running" {
			busy = append(busy, r.Name)
		}
	}
	if len(busy) > 0 {
		return fmt.Errorf("a setup runs on %s: restart the daemon when it has ended", strings.Join(busy, ", "))
	}
	return nil
}
