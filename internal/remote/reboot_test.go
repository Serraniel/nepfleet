// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRebootPlan(t *testing.T) {
	steps, err := RebootPlan(&Report{Kernel: "Linux", Sudo: true})
	if err != nil || len(steps) != 3 {
		t.Fatalf("%+v %v", steps, err)
	}
	if steps[0].Root || !steps[1].Root || steps[2].Root {
		t.Error("only the reboot itself runs as root")
	}
	if !strings.Contains(steps[1].Script, "--on-active=5") || steps[2].Wait != RebootWait || !strings.Contains(steps[2].Script, "boot_id") {
		t.Errorf("steps: %+v", steps)
	}
	if _, err := RebootPlan(&Report{Kernel: "Linux"}); err == nil {
		t.Error("planned without root or sudo")
	}
}

// A step with Wait runs again until it succeeds, and gives up after Wait.
func TestRunStepWaits(t *testing.T) {
	oldEvery, oldTry := waitEvery, waitTry
	waitEvery, waitTry = 10*time.Millisecond, time.Second
	t.Cleanup(func() { waitEvery, waitTry = oldEvery, oldTry })
	tries := 0
	flaky := func(context.Context, string) ([]byte, error) {
		tries++
		if tries < 3 {
			return nil, errors.New("not restarted yet")
		}
		return nil, nil
	}
	if err := runStep(context.Background(), flaky, "x", time.Second); err != nil || tries != 3 {
		t.Errorf("err %v after %d tries", err, tries)
	}
	never := func(context.Context, string) ([]byte, error) { return nil, errors.New("Docker is not running yet") }
	err := runStep(context.Background(), never, "x", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "still not done") || !strings.Contains(err.Error(), "Docker is not running yet") {
		t.Errorf("gave up with %v", err)
	}
	once := 0
	if err := runStep(context.Background(), func(context.Context, string) ([]byte, error) { once++; return nil, errors.New("x") }, "x", 0); err == nil || once != 1 {
		t.Error("a step without Wait runs once")
	}
}
