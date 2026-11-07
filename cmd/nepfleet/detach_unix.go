// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach lets a started process outlive this one and its terminal.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
