// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"net"
	"os"

	"github.com/Serraniel/nepfleet/internal/control"
)

// canHandOff: Windows has no exec, and keeps a running binary from being
// replaced anyway; restarts stop and start the daemon.
const canHandOff = false

type handoff struct {
	Addr      string
	AgentSock string
	AgentPID  int
	AuthSock  string

	lock       *os.File
	sock, port net.Listener
}

func adoptHandoff() (*handoff, error) { return nil, nil }

func execHandoff(string, handoff, *os.File, *control.Server) error {
	return errors.New("restarting in place isn't possible on Windows")
}
