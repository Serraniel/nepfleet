// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"testing"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/deploy"
)

func TestCheckReviewed(t *testing.T) {
	p := &deploy.Plan{Stack: "web", Changes: []deploy.Change{{Path: ".env", Kind: deploy.Changed, Diff: "+ PW='•••••• (abc123)'\n"}}}
	ap, err := apiPlanOf(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkReviewed(p, ap.ID, false); err != nil {
		t.Errorf("the reviewed plan: %v", err)
	}
	// New generated secrets get new marks; the plan is the same.
	same := &deploy.Plan{Stack: "web", Changes: []deploy.Change{{Path: ".env", Kind: deploy.Changed, Diff: "+ PW='•••••• (def456)'\n"}}}
	if err := checkReviewed(same, ap.ID, false); err != nil {
		t.Errorf("another secret mark: %v", err)
	}
	other := &deploy.Plan{Stack: "web", Changes: []deploy.Change{{Path: "compose.yaml", Kind: deploy.Changed, Diff: "+ x\n"}}}
	if err := checkReviewed(other, ap.ID, true); !errors.Is(err, api.ErrPlanChanged) {
		t.Errorf("a changed plan: %v", err)
	}
	d := &deploy.Plan{Stack: "web", Destructive: []string{`removes volume "data"`}}
	dp, _ := apiPlanOf(d)
	if err := checkReviewed(d, dp.ID, false); !errors.Is(err, api.ErrNeedsConfirm) {
		t.Errorf("unconfirmed destructive plan: %v", err)
	}
	if err := checkReviewed(d, dp.ID, true); err != nil {
		t.Errorf("confirmed destructive plan: %v", err)
	}
}

func TestLockStack(t *testing.T) {
	dir := t.TempDir()
	release, err := lockStack(dir, "web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockStack(dir, "web"); err == nil {
		t.Error("a second apply of a stack must be refused")
	}
	other, err := lockStack(dir, "db")
	if err != nil {
		t.Errorf("another stack: %v", err)
	} else {
		other()
	}
	release()
	again, err := lockStack(dir, "web")
	if err != nil {
		t.Errorf("after release: %v", err)
	} else {
		again()
	}
}
