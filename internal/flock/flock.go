// SPDX-License-Identifier: AGPL-3.0-or-later

// Package flock takes exclusive locks on files across processes. The system
// drops a lock when its process ends, so a crash leaves nothing behind.
package flock

import (
	"errors"
	"os"
)

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("the lock is held")

// Lock takes an exclusive lock on path, creating the file. With wait it blocks
// until the lock is free, otherwise it returns ErrHeld. The result releases it.
func Lock(path string, wait bool) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // callers pass their own state files
	if err != nil {
		return nil, err
	}
	if err := lockFile(f, wait); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
