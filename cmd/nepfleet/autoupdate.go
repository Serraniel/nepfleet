// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/notify"
)

// updateAction is what to do about a stack's images on this run.
type updateAction int

const (
	updateNothing updateAction = iota
	updateNotify
	updateApply
	updateWait // something to apply, but not now
)

// decideUpdate looks at a stack's setting and what the registry has. A rebuilt tag
// can apply by itself: the compose file and lock say which tag, and only its digest
// moves. A newer tag comes with a template update, which a person reviews.
func decideUpdate(mode string, updates []imageUpdate, inWindow, dirty bool) updateAction {
	rebuilt := slices.ContainsFunc(updates, func(u imageUpdate) bool { return u.Rebuilt })
	newer := slices.ContainsFunc(updates, func(u imageUpdate) bool { return u.Next != "" })
	switch {
	case mode == "off" || (!rebuilt && !newer):
		return updateNothing
	case mode != "auto" || !rebuilt:
		return updateNotify
	case dirty || !inWindow:
		return updateWait
	}
	return updateApply
}

// updateSummary says what's new, and is how a repeat is recognised.
func updateSummary(updates []imageUpdate) string {
	var parts []string
	for _, u := range updates {
		if u.Rebuilt {
			parts = append(parts, fmt.Sprintf("%s:%s was rebuilt", u.Image, u.Current))
		}
		if u.Next != "" {
			parts = append(parts, fmt.Sprintf("%s %s → %s", u.Image, u.Current, u.Next))
		}
	}
	slices.Sort(parts)
	return strings.Join(parts, ", ")
}

// notifiedOnce remembers what was last said per stack, so a timer doesn't repeat it.
func notifiedOnce(stateDir, stack, summary string) (bool, error) {
	path := filepath.Join(stateDir, "updates", "notified.json")
	seen := map[string]string{}
	if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // the installation's own state directory
		if json.Unmarshal(raw, &seen) != nil {
			seen = map[string]string{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return true, err
	}
	if seen[stack] == summary {
		return false, nil
	}
	seen[stack] = summary
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return true, err
	}
	raw, err := json.Marshal(seen)
	if err != nil {
		return true, err
	}
	return true, os.WriteFile(path, raw, 0o600)
}

// inUpdateWindow says whether updates: auto may act now.
func (e *env) inUpdateWindow(now time.Time) (bool, error) {
	w := e.fleet.Spec.Updates.Window
	if w == nil {
		return true, nil
	}
	p, err := notify.ParsePolicy("", w.From, w.To, w.Timezone)
	if err != nil {
		return false, err
	}
	return p.InWindow(now), nil
}

// cmdUpdatesApply is what a timer runs: stacks with updates: auto take rebuilt
// images, and others hear about what's new.
func cmdUpdatesApply(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("updates apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	now := fs.Bool("now", false, "ignore spec.updates.window")
	wait := fs.Duration("wait", applyWait, "how long a new version gets to become healthy")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runUpdates(ctx, c, fs.Args(), *now, *wait, stdout, stderr)
}

// runUpdates is updates apply: stacks with updates: auto take rebuilt images
// (in the window, unless now), and the others' news is sent once.
func runUpdates(ctx context.Context, c common, names []string, now bool, wait time.Duration, stdout, stderr io.Writer) error {
	e, err := c.open()
	if err != nil {
		return err
	}
	stacks, err := e.stacks(names)
	if err != nil {
		return err
	}
	e.resolverOnce()
	n, err := e.notifier()
	if err != nil {
		return err
	}
	inWindow, err := e.inUpdateWindow(time.Now())
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range stacks {
		name := s.Metadata.Name
		if s.Spec.Updates == "off" {
			continue
		}
		updates, err := e.stackUpdates(ctx, s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rel := relTo(e.fleet.Dir, s.Dir)
		status, _ := gitOut(e.fleet.Dir, "status", "--porcelain", "--", rel)
		summary := updateSummary(updates)
		switch decideUpdate(s.Spec.Updates, updates, inWindow || now, strings.TrimSpace(status) != "") {
		case updateNothing:
			continue
		case updateNotify:
			first, err := notifiedOnce(e.stateDir, name, summary)
			if err != nil {
				fmt.Fprintf(stderr, "%s: ⚠ %v\n", name, err)
			}
			if first {
				fmt.Fprintf(stdout, "%s: %s\n", name, summary)
				e.emit(n, stdout, notify.Event{Name: "update.available", Severity: notify.Info, Stack: name, Title: name + " has updates", Message: summary})
			}
		case updateWait:
			fmt.Fprintf(stdout, "%s: %s, waiting for the update window or for its folder to be committed\n", name, summary)
		case updateApply:
			fmt.Fprintf(stdout, "%s: %s; updating\n", name, summary)
			// Never by itself: anything that needs someone to say yes waits for them.
			check := func(p *deploy.Plan) error {
				if len(p.Destructive) > 0 {
					return fmt.Errorf("has changes that need a person: %s", strings.Join(p.Destructive, ", "))
				}
				return nil
			}
			if err := runApply(ctx, c, []string{name}, applyOptions{yes: true, wait: wait, updateLock: true, check: check}, stdout, stderr); err != nil {
				errs = append(errs, err)
				continue
			}
			commitAPIApply(c, name, "automatic updates", stdout)
			e.emit(n, stdout, notify.Event{Name: "update.applied", Severity: notify.Info, Stack: name, Title: name + " updated", Message: summary})
		}
	}
	return errors.Join(errs...)
}

// startUpdates is POST /api/v1/updates/run: updates apply as a job.
func startUpdates(ctx context.Context, c common, now bool, by string) (func(io.Writer) error, error) {
	if !apiMu.TryLock() {
		return nil, api.ErrBusy
	}
	return func(log io.Writer) error {
		defer apiMu.Unlock()
		fmt.Fprintf(log, "updates, requested by %s over the API\n", by)
		return runUpdates(ctx, c, nil, now, applyWait, log, log)
	}, nil
}
