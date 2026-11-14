// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os/signal"
	"slices"
	"syscall"
)

// changing are the commands that change machines or the fleet repository.
// Go ends a program whose output pipe closed (nepfleet apply | head), which
// would stop them halfway; they keep going, and their output goes nowhere.
var changing = []string{"apply", "rollback", "restore", "new", "enable", "import", "mirror", "secret", "key", "updates", "host", "remote", "serve", "control"}

// keepGoingWhenOutputCloses ignores SIGPIPE for changing commands. Read-only
// ones (plan, watch, render…) still end when nobody reads them.
func keepGoingWhenOutputCloses(args []string) bool {
	if len(args) == 0 || !slices.Contains(changing, args[0]) {
		return false
	}
	signal.Ignore(syscall.SIGPIPE)
	return true
}
