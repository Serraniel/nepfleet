// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/notify"
)

// The reconcile loop applies stacks set to apply: auto whose plan has
// changes, whatever brought them: an edit committed on the server, a newer
// NepFleet rendering them differently (new built-in values), or their
// template source. Git sync applies what it pulls; this covers what no pull
// brings. Destructive changes and migrations wait for a person, and a plan
// that failed is not tried again until it changes.

// reconcileEvery is how often the loop looks, and how long after the server
// starts it looks first (a NepFleet update restarts the server).
const reconcileEvery = 15 * time.Minute

// reconcileAuthor commits what an automatic apply records (lock.yaml).
const reconcileAuthor = "nepfleet"

// updatesEvery is how often serve takes image updates (updates apply) for
// stacks with updates: auto, within fleet.yaml's updates.window.
const updatesEvery = 6 * time.Hour

type reconciler struct {
	c     common
	every time.Duration
	// updates: how often image updates run; 0 never.
	updates time.Duration
	log     io.Writer
	kick    chan struct{}
	// adopted: the written-out defaults were looked at (once per start).
	adopted bool

	mu sync.Mutex
	// held: per stack, the plan that failed or waits for a person. It isn't
	// tried again, nor announced again, until the plan changes.
	held map[string]string
}

func newReconciler(c common, every time.Duration, log io.Writer) *reconciler {
	return &reconciler{c: c, every: every, log: log, kick: make(chan struct{}, 1), held: map[string]string{}}
}

// Now asks for a round soon, e.g. after an edit was committed.
func (r *reconciler) Now() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// run reconciles shortly after start, on the timer and on Now, until ctx ends.
func (r *reconciler) run(ctx context.Context) {
	t := time.NewTicker(r.every)
	defer t.Stop()
	first := time.NewTimer(time.Minute)
	defer first.Stop()
	// Image updates: ten minutes after start, then every r.updates.
	var updatesTick <-chan time.Time
	upd := time.NewTimer(10 * time.Minute)
	defer upd.Stop()
	if r.updates <= 0 {
		upd.Stop()
	} else {
		ut := time.NewTicker(r.updates)
		defer ut.Stop()
		updatesTick = ut.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-upd.C:
			if !r.takeUpdates(ctx) {
				upd.Reset(time.Minute)
			}
			continue
		case <-updatesTick:
			if !r.takeUpdates(ctx) {
				upd.Reset(time.Minute)
			}
			continue
		case <-first.C:
		case <-t.C:
		case <-r.kick:
		}
		if !r.once(ctx) {
			// The API is applying something: look again soon.
			first.Reset(time.Minute)
		}
	}
}

// takeUpdates runs updates apply: stacks with updates: auto take rebuilt
// images, health-gated, and the others hear what's new. It reports false
// when the API was busy.
func (r *reconciler) takeUpdates(ctx context.Context) bool {
	if !apiMu.TryLock() {
		return false
	}
	defer apiMu.Unlock()
	if err := runUpdates(ctx, r.c, nil, false, applyWait, r.log, r.log); err != nil {
		fmt.Fprintf(r.log, "updates: %v\n", err)
	}
	return true
}

// once looks at every auto stack on this host once. It reports false when the
// API was busy and nothing was looked at.
func (r *reconciler) once(ctx context.Context) bool {
	if !apiMu.TryLock() {
		return false
	}
	defer apiMu.Unlock()
	e, err := r.c.open()
	if err != nil {
		fmt.Fprintf(r.log, "reconcile: %v\n", err)
		return true
	}
	if !r.adopted {
		r.adopted = true
		adoptDefaults(e, r.log)
	}
	all, err := e.fleet.Stacks()
	if err != nil {
		fmt.Fprintf(r.log, "reconcile: %v\n", err)
		return true
	}
	// A repository that syncs from origin takes changes from there: a
	// commit here would stop sync as diverged.
	_, upstreamErr := gitOut(e.fleet.Dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	synced := upstreamErr == nil
	for _, s := range all {
		if ctx.Err() != nil {
			return true
		}
		name := s.Metadata.Name
		if s.Spec.Target != "local" && s.Spec.Target != e.host {
			continue
		}
		// A newer template: auto stacks move to it within a major version,
		// with its image updates; the rest is announced once.
		takeUpdates := false
		if moved, err := r.templateMove(e, s, synced); err != nil {
			fmt.Fprintf(r.log, "reconcile: %s: template: %v\n", name, err)
		} else if moved {
			if s, err = e.fleet.LoadStack(name); err != nil {
				fmt.Fprintf(r.log, "reconcile: %s: %v\n", name, err)
				continue
			}
			takeUpdates = true
		}
		if s.Spec.Apply != "auto" {
			continue
		}
		e.updateLock = takeUpdates
		p, _, err := e.preparePlan(ctx, s, io.Discard)
		e.updateLock = false
		if err != nil {
			r.hold(e, name, "error:"+err.Error(), notify.Event{Name: "apply.pending", Severity: notify.Warning, Stack: name,
				Title: name + " can't be planned", Message: cleanReason(err, e.fleet.Dir)})
			continue
		}
		pl := p.plan
		// Never applied: creating a stack is a person's step.
		if pl.First || pl.Empty() {
			r.release(name)
			continue
		}
		key := planKey(pl)
		if r.isHeld(name, key) {
			continue
		}
		if reason := needsPerson(pl); reason != "" {
			r.hold(e, name, key, notify.Event{Name: "apply.pending", Severity: notify.Warning, Stack: name,
				Title: name + " waits for you", Message: reason + ": review its plan and apply it."})
			continue
		}
		fmt.Fprintf(r.log, "reconcile: applying %s\n", name)
		o := applyOptions{yes: true, wait: applyWait, updateLock: takeUpdates, check: func(pl *deploy.Plan) error {
			if needsPerson(pl) != "" {
				return errNeedsPerson
			}
			return nil
		}}
		// runApply announces success and failure itself (apply.succeeded,
		// apply.failed, apply.rolled_back).
		if err := runApply(ctx, r.c, []string{name}, o, r.log, r.log); err != nil {
			// Changed between looking and applying: the next round sees it.
			if !errors.Is(err, errNeedsPerson) {
				r.holdQuiet(name, key)
				fmt.Fprintf(r.log, "reconcile: %s: %v\n", name, err)
			}
			continue
		}
		r.release(name)
		commitAPIApply(r.c, name, reconcileAuthor, r.log)
	}
	return true
}

// templateMove moves an auto stack to a newer template version of the same
// major, committing the new pin, and reports whether it did. A newer major,
// a manual stack, or a repository that syncs from origin (the move belongs
// there) is announced once instead.
func (r *reconciler) templateMove(e *env, s *fleet.Stack, synced bool) (bool, error) {
	u, err := e.newerTemplate(s)
	if err != nil || u == nil {
		return false, err
	}
	name := s.Metadata.Name
	if s.Spec.Apply == "auto" && u.sameMajor && !synced {
		fmt.Fprintf(r.log, "reconcile: %s: template %s %s → %s\n", name, s.Spec.Template.Name, u.from, u.to)
		return true, e.moveTemplate(s, u, reconcileAuthor)
	}
	why := "review it and set spec.template.version to " + u.to
	switch {
	case !u.sameMajor:
		why = "a new major version can change data: " + why + ", and its plan shows the migration"
	case synced:
		why = "the fleet repository syncs from origin: set spec.template.version to " + u.to + " there"
	}
	r.hold(e, name+"#template", u.to, notify.Event{Name: "update.available", Severity: notify.Info, Stack: name,
		Title:   fmt.Sprintf("%s: template %s %s is out", name, s.Spec.Template.Name, u.to),
		Message: fmt.Sprintf("It runs %s; %s.", u.from, why)})
	return false, nil
}

// needsPerson says why a plan can't apply by itself, or "".
func needsPerson(pl *deploy.Plan) string {
	switch {
	case pl.MigrationError != "":
		return pl.MigrationError
	case pl.Migration != nil:
		return fmt.Sprintf("it migrates data from template version %s to %s", pl.Migration.From, pl.Migration.To)
	case len(pl.Destructive) > 0:
		return "its plan has destructive changes"
	}
	return ""
}

// planKey identifies a plan's changes: the same key is the same plan.
func planKey(pl *deploy.Plan) string {
	raw, _ := json.Marshal(struct {
		Template string
		Changes  []deploy.Change
	}{pl.Template, pl.Changes})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (r *reconciler) isHeld(name, key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.held[name] == key
}

func (r *reconciler) holdQuiet(name, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.held[name] = key
}

// hold keeps a stack's plan from being tried again, and announces it once.
func (r *reconciler) hold(e *env, name, key string, ev notify.Event) {
	if r.isHeld(name, key) {
		return
	}
	r.holdQuiet(name, key)
	fmt.Fprintf(r.log, "reconcile: %s: %s\n", name, ev.Message)
	n, err := e.notifier()
	if err != nil {
		n = nil
	}
	e.emit(n, r.log, ev)
}

func (r *reconciler) release(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.held, name)
}
