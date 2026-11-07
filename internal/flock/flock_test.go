// SPDX-License-Identifier: AGPL-3.0-or-later

package flock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	release, err := Lock(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(path, false); !errors.Is(err, ErrHeld) {
		t.Errorf("a second lock: %v", err)
	}
	release()
	again, err := Lock(path, true)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}
