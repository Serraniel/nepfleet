// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !windows

package main

import (
	"encoding/json"
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
	// Closed on exec again: every ssh, git or docker this daemon runs would
	// otherwise hold the lock too, and one left behind by a crash would keep
	// any daemon from starting.
	if err := closeOnExec(h.lock, true); err != nil {
		return nil, err
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

// rawFd is f's descriptor number. Not f.Fd(): that switches the descriptor
// to blocking, and for a listener's duplicate also the listener itself (they
// share the open file), which then hangs if this daemon has to carry on.
func rawFd(f *os.File) (int, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return 0, err
	}
	n := -1
	if err := rc.Control(func(fd uintptr) { n = int(fd) }); err != nil { //nolint:gosec // descriptors fit an int
		return 0, err
	}
	return n, nil
}

// closeOnExec sets or clears FD_CLOEXEC on f.
func closeOnExec(f *os.File, on bool) error {
	fd, err := rawFd(f)
	if err != nil {
		return err
	}
	flag := 0
	if on {
		flag = unix.FD_CLOEXEC
	}
	_, err = unix.FcntlInt(uintptr(fd), unix.F_SETFD, flag)
	return err
}

// handoffExecTarget, when set (tests only), is exec'd instead of the binary.
var handoffExecTarget string

// execHandoff replaces this process by exe, the binary installed now, after
// Serve ended for a handover. It returns only on failure, with this process
// as it was: the lock closes on exec again, and the listeners stay open in s
// (control.Server.ResumeAfterHandoff) so the daemon can carry on.
func execHandoff(exe string, h handoff, lock *os.File, s *control.Server) (err error) {
	if handoffExecTarget != "" {
		exe = handoffExecTarget
	}
	sock, port, addr, err := s.Listeners()
	if err != nil {
		return err
	}
	files := []*os.File{sock}
	if port != nil {
		files = append(files, port)
	}
	defer func() {
		// Only reached when exec failed: the duplicates go, the lock stays ours.
		for _, f := range files {
			_ = f.Close()
		}
		_ = closeOnExec(lock, true)
	}()
	for _, f := range append([]*os.File{lock}, files...) {
		// Duplicates close on exec by default; these must survive it.
		if err := closeOnExec(f, false); err != nil {
			return err
		}
	}
	if h.Lock, err = rawFd(lock); err != nil {
		return err
	}
	if h.Sock, err = rawFd(sock); err != nil {
		return err
	}
	if port != nil {
		if h.Port, err = rawFd(port); err != nil {
			return err
		}
		h.Addr = addr
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
	return fmt.Errorf("starting %s: %w", exe, err)
}

func hasKey(kv, key string) bool {
	return len(kv) > len(key) && kv[:len(key)] == key && kv[len(key)] == '='
}
