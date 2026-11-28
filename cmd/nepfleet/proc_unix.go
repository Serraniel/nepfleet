// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// processGone tells whether no process with this pid is left.
func processGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
