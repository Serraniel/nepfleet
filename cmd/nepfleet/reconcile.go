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

type reconciler struct {
	c     common
	every time.Duration
	log   io.Writer
	kick  chan struct{}

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
	for {
		select {
		case <-ctx.Done():
			return
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
	all, err := e.fleet.Stacks()
	if err != nil {
		fmt.Fprintf(r.log, "reconcile: %v\n", err)
		return true
	}
	for _, s := range all {
		if ctx.Err() != nil {
			return true
		}
		name := s.Metadata.Name
		if s.Spec.Apply != "auto" || (s.Spec.Target != "local" && s.Spec.Target != e.host) {
			continue
		}
		p, err := e.prepare(s)
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
		o := applyOptions{yes: true, wait: applyWait, check: func(pl *deploy.Plan) error {
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
