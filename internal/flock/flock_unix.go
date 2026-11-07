// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package flock

import (
	"errors"
	"os"
	"syscall"
)

func lockFile(f *os.File, wait bool) error {
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	err := syscall.Flock(int(f.Fd()), how) //nolint:gosec // a file descriptor fits an int
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrHeld
	}
	return err
}
