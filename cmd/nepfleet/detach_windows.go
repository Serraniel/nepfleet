// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detach lets a started process outlive this one and its console.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}
