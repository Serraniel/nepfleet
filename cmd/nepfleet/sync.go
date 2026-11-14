// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/notify"
)

// Git sync keeps the server's fleet repository at its origin's commit: on a
// timer (serve --sync) or a forge's webhook it fetches, fast-forwards only, and
// applies the changed stacks set to apply: auto. The others wait for a person.

// syncer runs one sync at a time and remembers how the last one went.
type syncer struct {
	c        common
	interval time.Duration
	log      io.Writer
	kick     chan struct{}

	mu     sync.Mutex
	status api.SyncStatus
}

func newSyncer(c common, interval time.Duration, log io.Writer) *syncer {
	s := &syncer{c: c, interval: interval, log: log, kick: make(chan struct{}, 1)}
	s.status.Enabled = true
	if interval > 0 {
		s.status.Interval = interval.String()
	}
	return s
}

// Now asks for a sync soon; a sync already asked for counts once.
func (s *syncer) Now() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Status is how the last sync went.
func (s *syncer) Status() api.SyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	st.Applied = slices.Clone(st.Applied)
	st.Failed = slices.Clone(st.Failed)
	st.Pending = slices.Clone(st.Pending)
	return st
}

// run syncs on the timer and on Now, until ctx ends.
func (s *syncer) run(ctx context.Context) {
	var tick <-chan time.Time
	if s.interval > 0 {
		t := time.NewTicker(s.interval)
		defer t.Stop()
		tick = t.C
		s.Now() // once at start, too
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-s.kick:
		}
		s.once(ctx)
	}
}

// pulled is what a fast-forward brought in.
type pulled struct {
	from, to string
	stacks   []string
}

var errDiverged = errors.New("the fleet repository has commits that origin doesn't: sync only fast-forwards, so it stopped; push or reset them")

// pullFleet fast-forwards the fleet repository to its upstream, and names the
// stacks the new commits changed. A change to fleet.yaml counts for all.
func pullFleet(ctx context.Context, e *env) (pulled, error) {
	dir := e.fleet.Dir
	git := func(args ...string) (string, error) {
		out, err := gitOut(dir, args...)
		return strings.TrimSpace(out), err
	}
	if _, err := git("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err != nil {
		return pulled{}, errors.New("the fleet repository's branch tracks no upstream (git branch --set-upstream-to origin/main)")
	}
	if st, _ := git("status", "--porcelain", "--untracked-files=no"); st != "" {
		return pulled{}, errors.New("the fleet repository has uncommitted changes on the server: commit or discard them, then sync")
	}
	if ctx.Err() != nil {
		return pulled{}, ctx.Err()
	}
	if _, err := git("fetch", "--quiet"); err != nil {
		return pulled{}, err
	}
	from, err := git("rev-parse", "HEAD")
	if err != nil {
		return pulled{}, err
	}
	to, err := git("rev-parse", "@{u}")
	if err != nil {
		return pulled{}, err
	}
	if from == to {
		return pulled{from: from, to: to}, nil
	}
	if _, err := git("merge-base", "--is-ancestor", "HEAD", "@{u}"); err != nil {
		return pulled{from: from}, errDiverged
	}
	if _, err := git("merge", "--ff-only", "--quiet", "@{u}"); err != nil {
		return pulled{from: from}, err
	}
	changed, err := git("diff", "--name-only", from, to)
	if err != nil {
		return pulled{from: from, to: to}, err
	}
	all, err := e.fleet.Stacks()
	if err != nil {
		return pulled{from: from, to: to}, err
	}
	everything := false
	touched := map[string]bool{}
	for _, f := range strings.Split(changed, "\n") {
		if f == "fleet.yaml" || f == fleet.FleetSecretsFile {
			everything = true
		}
		for _, s := range all {
			if rel := relTo(dir, s.Dir) + "/"; strings.HasPrefix(f, rel) {
				touched[s.Metadata.Name] = true
			}
		}
	}
	var names []string
	for _, s := range all {
		if everything || touched[s.Metadata.Name] {
			names = append(names, s.Metadata.Name)
		}
	}
	sort.Strings(names)
	return pulled{from: from, to: to, stacks: names}, nil
}

var errNeedsPerson = errors.New("destructive changes wait for a person")

// once pulls and applies what changed. A running apply of the API makes it
// wait for the next turn.
func (s *syncer) once(ctx context.Context) {
	if !apiMu.TryLock() {
		return
	}
	defer apiMu.Unlock()
	e, err := s.c.open()
	if err != nil {
		s.finish(api.SyncStatus{Error: err.Error()})
		return
	}
	p, err := pullFleet(ctx, e)
	st := api.SyncStatus{Commit: p.to}
	if err != nil {
		st.Error = err.Error()
		if st.Commit == "" {
			st.Commit = p.from
		}
		s.finish(st)
		if errors.Is(err, errDiverged) {
			s.emit(e, notify.Event{Name: "sync.failed", Severity: notify.Warning, Title: "Fleet sync stopped", Message: err.Error()})
		}
		return
	}
	for _, name := range p.stacks {
		stack, err := e.fleet.LoadStack(name)
		if err != nil {
			st.Failed = append(st.Failed, name)
			continue
		}
		if stack.Spec.Target != "local" && stack.Spec.Target != e.host {
			continue // another host's NepFleet applies it
		}
		if stack.Spec.Apply != "auto" {
			st.Pending = append(st.Pending, name)
			s.emit(e, notify.Event{Name: "sync.pending", Severity: notify.Info, Stack: name,
				Title: name + " changed", Message: "Pulled " + short(p.to) + ": review its plan and apply it."})
			continue
		}
		fmt.Fprintf(s.log, "sync: applying %s at %s\n", name, short(p.to))
		o := applyOptions{yes: true, wait: applyWait, check: func(pl *deploy.Plan) error {
			if len(pl.Destructive) > 0 {
				return errNeedsPerson
			}
			return nil
		}}
		err = runApply(ctx, s.c, []string{name}, o, s.log, s.log)
		switch {
		case errors.Is(err, errNeedsPerson):
			st.Pending = append(st.Pending, name)
			s.emit(e, notify.Event{Name: "sync.pending", Severity: notify.Warning, Stack: name,
				Title: name + " has destructive changes", Message: "Pulled " + short(p.to) + ": its plan removes something, so it waits for a person to confirm."})
		case err != nil:
			st.Failed = append(st.Failed, name)
			s.emit(e, notify.Event{Name: "sync.failed", Severity: notify.Error, Stack: name,
				Title: name + " failed to apply", Message: cleanReason(err, e.fleet.Dir)})
		default:
			st.Applied = append(st.Applied, name)
		}
	}
	s.finish(st)
}

func (s *syncer) finish(st api.SyncStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.Enabled, st.Interval, st.Last = s.status.Enabled, s.status.Interval, time.Now().UTC()
	if st.Commit == "" {
		st.Commit = s.status.Commit
	}
	s.status = st
}

func (s *syncer) emit(e *env, ev notify.Event) {
	n, err := e.notifier()
	if err != nil {
		n = nil
	}
	e.emit(n, s.log, ev)
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
