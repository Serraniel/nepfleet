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
	var old controlInfo
	if controlCall(ctx, http.MethodGet, "/v1/info", nil, &old) == nil {
		if err := stopControl(ctx, old); err != nil {
			return err
		}
	}
	if err := ensureControl(ctx); err != nil {
		return err
	}
	var info controlInfo
	if err := controlCall(ctx, http.MethodGet, "/v1/info", nil, &info); err != nil {
		return err
	}
	printControlInfo(stdout, info)
	return nil
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
	for range 150 {
		if controlCall(ctx, http.MethodGet, "/v1/info", nil, nil) != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the control daemon (pid %d) did not stop", info.PID)
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
