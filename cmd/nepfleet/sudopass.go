// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

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
	if fromStdin {
		line, err := bufio.NewReader(stdinReader).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("reading the sudo password from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fd := int(os.Stdin.Fd()) //nolint:gosec // file descriptors fit in int
	if !term.IsTerminal(fd) {
		return "", fmt.Errorf("%w (with no terminal to ask on, pass it with --sudo-password-stdin)", remote.ErrPasswordNeeded)
	}
	user := r.User
	if user == "" {
		user = t.User
	}
	fmt.Fprintf(stdout, "[sudo] password for %s on %s: ", user, t.Host)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(stdout)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", remote.ErrPasswordNeeded
	}
	return string(b), nil
}
