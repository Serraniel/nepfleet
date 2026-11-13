// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/Serraniel/nepfleet/internal/remote"
)

// stdinReader is where --sudo-password-stdin reads; tests replace it.
var stdinReader io.Reader = os.Stdin

// sudoPassword asks for the sudo password when the plan's root steps need it:
// on the terminal without echo, as sudo does, or from stdin for scripts. It is
// used for this run only.
func sudoPassword(r *remote.Report, steps []remote.Step, t remote.Target, fromStdin bool, stdout io.Writer) (string, error) {
	if !remote.NeedsSudoPassword(r, steps) {
		return "", nil
	}
	fd := int(os.Stdin.Fd()) //nolint:gosec // file descriptors fit in int
	interactive := term.IsTerminal(fd)
	// From stdin for scripts; typed at a terminal, it is read without echo below.
	if fromStdin && !interactive {
		line, err := bufio.NewReader(stdinReader).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("reading the sudo password from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	if !interactive {
		return "", fmt.Errorf("%w (with no terminal to ask on, pass it with --sudo-password-stdin and --yes)", remote.ErrPasswordNeeded)
	}
	user := r.User
	if user == "" {
		user = t.User
	}
	fmt.Fprintf(stdout, "[sudo] password for %s on %s: ", user, t.Host)
	b, err := readHidden(fd)
	fmt.Fprintln(stdout)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", remote.ErrPasswordNeeded
	}
	return string(b), nil
}

// readHidden reads a line without echo. Ctrl-C leaves the terminal as it was:
// term.ReadPassword alone would leave echo off when the process ends there.
func readHidden(fd int) ([]byte, error) {
	state, err := term.GetState(fd)
	if err != nil {
		return nil, err
	}
	interrupt := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-interrupt:
			_ = term.Restore(fd, state)
			fmt.Fprintln(os.Stderr)
			os.Exit(130)
		case <-done:
		}
	}()
	defer func() {
		signal.Stop(interrupt)
		close(done)
	}()
	return term.ReadPassword(fd)
}
