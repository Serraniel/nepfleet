// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/Serraniel/nepfleet/internal/control"
	"github.com/Serraniel/nepfleet/internal/flock"
)

// canHandOff: here a daemon can replace itself by the binary installed now
// (exec), keeping its pid, lock, socket, port and SSH agent.
const canHandOff = true

// handoffEnv carries a handoff to the exec'd daemon, and only to it.
const handoffEnv = "NEPFLEET_CONTROL_HANDOFF"

// handoff is what a daemon passes on: inherited file descriptors, the port's
// address, its own SSH agent, and the session's SSH_AUTH_SOCK from before it
// pointed that at its own agent.
type handoff struct {
	Lock      int    `json:"lock"`
	Sock      int    `json:"sock"`
	Port      int    `json:"port,omitempty"`
	Addr      string `json:"addr,omitempty"`
	AgentSock string `json:"agentSock,omitempty"`
	AgentPID  int    `json:"agentPid,omitempty"`
	AuthSock  string `json:"authSock,omitempty"`

	lock       *os.File
	sock, port net.Listener
}

// adoptHandoff takes over from the daemon before, if this process is its exec.
func adoptHandoff() (*handoff, error) {
	raw := os.Getenv(handoffEnv)
	if raw == "" {
		return nil, nil
	}
	_ = os.Unsetenv(handoffEnv) // not for anything this daemon runs
	var h handoff
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return nil, fmt.Errorf("%s: %w", handoffEnv, err)
	}
	h.lock = os.NewFile(uintptr(h.Lock), "control.lock") //nolint:gosec // a descriptor number fits
	if err := flock.Adopt(h.lock); err != nil {
		return nil, fmt.Errorf("the daemon before passed on no lock: %w", err)
	}
	var err error
	if h.sock, err = inherit(h.Sock, "control.sock"); err != nil {
		return nil, err
	}
	if h.Port != 0 {
		if h.port, err = inherit(h.Port, h.Addr); err != nil {
			return nil, err
		}
	}
	return &h, nil
}

func inherit(fd int, name string) (net.Listener, error) {
	f := os.NewFile(uintptr(fd), name) //nolint:gosec // a descriptor number fits
	defer func() { _ = f.Close() }()
	l, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("taking over %s: %w", name, err)
	}
	return l, nil
}

// execHandoff replaces this process by exe, the binary installed now, after
// Serve ended for a handover. It returns only on failure.
func execHandoff(exe string, h handoff, lock *os.File, s *control.Server) error {
	sock, port, addr, err := s.Listeners()
	if err != nil {
		return err
	}
	files := []*os.File{lock, sock}
	if port != nil {
		files = append(files, port)
	}
	for _, f := range files {
		// Duplicates close on exec by default; these must survive it.
		if _, err := unix.FcntlInt(f.Fd(), unix.F_SETFD, 0); err != nil {
			return err
		}
	}
	h.Lock, h.Sock = int(lock.Fd()), int(sock.Fd()) //nolint:gosec // descriptors fit an int
	if port != nil {
		h.Port, h.Addr = int(port.Fd()), addr //nolint:gosec // descriptors fit an int
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return err
	}
	env := []string{handoffEnv + "=" + string(raw)}
	for _, kv := range os.Environ() {
		if !hasKey(kv, "SSH_AUTH_SOCK") && !hasKey(kv, handoffEnv) {
			env = append(env, kv)
		}
	}
	if h.AuthSock != "" {
		env = append(env, "SSH_AUTH_SOCK="+h.AuthSock)
	}
	err = syscall.Exec(exe, os.Args, env) //nolint:gosec // this program, installed anew
	return errors.Join(errors.New("starting the updated nepfleet"), err)
}

func hasKey(kv, key string) bool {
	return len(kv) > len(key) && kv[:len(key)] == key && kv[len(key)] == '='
}
