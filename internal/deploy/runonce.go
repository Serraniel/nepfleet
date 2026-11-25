// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// runOncePoll is how often the wait checks whether run-once services finished.
var runOncePoll = time.Second

// runOnceServices splits a stack's services into run-once ones and the rest.
// A service is run-once when it says restart: "no" explicitly (a setup step
// that exits when done), or when another service waits for it with
// service_completed_successfully. An unset restart is "no" to Docker too, but
// hand-written stacks often leave it out on services that run for good:
// waiting for those to exit would never end.
func runOnceServices(ctx context.Context, r Runner, stack, dir string) (once, rest []string, err error) {
	out, err := r.Run(ctx, dir, compose(stack, dir, "config", "--format", "json")...)
	if err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil, nil
	}
	var cfg struct {
		Services map[string]struct {
			Restart   string `json:"restart"`
			DependsOn map[string]struct {
				Condition string `json:"condition"`
			} `json:"depends_on"`
		} `json:"services"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		return nil, nil, fmt.Errorf("reading the compose config: %w", err)
	}
	completes := map[string]bool{}
	for _, s := range cfg.Services {
		for dep, d := range s.DependsOn {
			if d.Condition == "service_completed_successfully" {
				completes[dep] = true
			}
		}
	}
	for name, s := range cfg.Services {
		if s.Restart == "no" || completes[name] {
			once = append(once, name)
		} else {
			rest = append(rest, name)
		}
	}
	slices.Sort(once)
	slices.Sort(rest)
	return once, rest, nil
}

// upCompose starts a compose stack and waits until it is healthy. mask hides
// secret values in what it adds to errors: they reach notifications.
//
// compose up --wait fails as soon as any container exits, even one that is
// meant to: a setup service that exits 0 when done, unless another service
// waits for it with service_completed_successfully. So with run-once
// services, start everything, let compose wait for the other services only
// (--no-deps leaves the finished ones alone), then wait for the run-once
// services to exit: 0 is done, anything else fails the apply with its log.
func upCompose(ctx context.Context, r Runner, stack, dir string, wait time.Duration, mask func(string) string) error {
	once, rest, err := runOnceServices(ctx, r, stack, dir)
	if err != nil {
		return err
	}
	if len(once) == 0 {
		_, err := r.Run(ctx, dir, compose(stack, dir, "up", "--detach", "--remove-orphans", "--wait",
			"--wait-timeout", fmt.Sprint(int(wait.Seconds())))...)
		return err
	}
	var deadline time.Time
	if wait > 0 {
		deadline = time.Now().Add(wait)
	}
	// up without --wait still waits for depends_on conditions, which may
	// never be met: bound it by the same wait.
	uctx, cancel := ctx, context.CancelFunc(func() {})
	if wait > 0 {
		uctx, cancel = context.WithDeadline(ctx, deadline)
	}
	_, err = r.Run(uctx, dir, compose(stack, dir, "up", "--detach", "--remove-orphans")...)
	timedOut := uctx.Err() != nil && ctx.Err() == nil
	cancel()
	if timedOut {
		// err carries the end of compose's output: what it was waiting for.
		return fmt.Errorf("%s did not start within %s: %s", stack, wait, mask(errText(err)))
	}
	if err != nil {
		// A run-once service others wait for with service_completed_successfully
		// failed: compose only says so, the log says why.
		if failed := runOnceFailure(ctx, r, stack, dir, once, mask); failed != nil {
			return fmt.Errorf("%w; %w", err, failed)
		}
		return err
	}
	if len(rest) > 0 {
		left := 0
		if wait > 0 {
			left = max(1, int(time.Until(deadline).Seconds()))
		}
		args := append([]string{"up", "--detach", "--no-deps", "--wait", "--wait-timeout", fmt.Sprint(left)}, rest...)
		if _, err := r.Run(ctx, dir, compose(stack, dir, args...)...); err != nil {
			return err
		}
	}
	return waitRunOnce(ctx, r, stack, dir, once, deadline, wait, mask)
}

func errText(err error) string {
	if err == nil {
		return "compose was still starting the services"
	}
	return err.Error()
}

// psEntry is one container of `compose ps --format json`.
type psEntry struct {
	Name     string
	Service  string
	State    string
	ExitCode int
}

// parsePS reads `compose ps --format json`: one object per line (Compose
// 2.21 and later) or one array (earlier).
func parsePS(out []byte) ([]psEntry, error) {
	out = bytes.TrimSpace(out)
	var list []psEntry
	if bytes.HasPrefix(out, []byte("[")) {
		return list, json.Unmarshal(out, &list)
	}
	for _, line := range bytes.Split(out, []byte("\n")) {
		if line = bytes.TrimSpace(line); len(line) == 0 {
			continue
		}
		var e psEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, err
		}
		list = append(list, e)
	}
	return list, nil
}

// runOnceState reads the containers of the run-once services, sorted by name.
func runOnceState(ctx context.Context, r Runner, stack, dir string, services []string) ([]psEntry, error) {
	out, err := r.Run(ctx, dir, compose(stack, dir, append([]string{"ps", "--all", "--format", "json"}, services...)...)...)
	if err != nil {
		return nil, err
	}
	list, err := parsePS(out)
	if err != nil {
		return nil, fmt.Errorf("reading the state of %s: %w", strings.Join(services, ", "), err)
	}
	slices.SortFunc(list, func(a, b psEntry) int { return strings.Compare(a.Name, b.Name) })
	return list, nil
}

// failed reports a container that stopped without succeeding.
func (c psEntry) failed() bool {
	return c.State == "dead" || c.State == "exited" && c.ExitCode != 0
}

// waitRunOnce waits until every container of the run-once services exited.
func waitRunOnce(ctx context.Context, r Runner, stack, dir string, services []string, deadline time.Time, wait time.Duration, mask func(string) string) error {
	for {
		list, err := runOnceState(ctx, r, stack, dir, services)
		if err != nil {
			return err
		}
		pending := ""
		seen := map[string]bool{}
		for _, c := range list {
			seen[c.Service] = true
			switch {
			case c.failed():
				return runOnceFailed(ctx, r, stack, dir, c, mask)
			case c.State != "exited" && pending == "":
				pending = c.Service
			}
		}
		for _, s := range services {
			if !seen[s] {
				return fmt.Errorf("run-once service %s has no container", s)
			}
		}
		if pending == "" {
			return nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("run-once service %s did not finish within %s", pending, wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(runOncePoll):
		}
	}
}

// runOnceFailure returns the failure of the first run-once service that
// failed, with its log, or nil when none did (or their state can't be read).
func runOnceFailure(ctx context.Context, r Runner, stack, dir string, services []string, mask func(string) string) error {
	list, err := runOnceState(ctx, r, stack, dir, services)
	if err != nil {
		return nil
	}
	for _, c := range list {
		if c.failed() {
			return runOnceFailed(ctx, r, stack, dir, c, mask)
		}
	}
	return nil
}

// runOnceFailed reports a run-once service that failed, with its last log
// lines, secrets masked.
func runOnceFailed(ctx context.Context, r Runner, stack, dir string, c psEntry, mask func(string) string) error {
	err := fmt.Errorf("run-once service %s exited with code %d", c.Service, c.ExitCode)
	if c.State == "dead" {
		err = fmt.Errorf("run-once service %s is dead", c.Service)
	}
	logs, lerr := r.Run(ctx, dir, compose(stack, dir, "logs", "--no-log-prefix", "--tail", "10", c.Service)...)
	if lerr != nil || len(bytes.TrimSpace(logs)) == 0 {
		return err
	}
	return fmt.Errorf("%w: %s", err, mask(tail(string(logs), 10)))
}
