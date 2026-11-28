// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "golang.org/x/sys/windows"

// processGone tells whether no process with this pid is left.
func processGone(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid)) //nolint:gosec // a pid fits
	if err != nil {
		return true
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == windows.WAIT_OBJECT_0
}
