// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

// Step is one thing bootstrapping a host does.
type Step struct {
	Text   string `json:"text"`
	Root   bool   `json:"root"`
	Script string `json:"-"`
}

var (
	networkRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
	unixRe    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// dockerInstall holds, per distribution, the commands that install Docker and
// the Compose plugin from its own packages.
func dockerInstall(osID string) (docker, compose string, ok bool) {
	const apt = "export DEBIAN_FRONTEND=noninteractive\napt-get update -qq\n"
	switch osID {
	case "arch", "manjaro", "endeavouros":
		// No -Sy: a partial upgrade breaks Arch. If the package database is too
		// old for this, pacman says so and the owner updates first.
		return "pacman -S --needed --noconfirm docker docker-compose\n", "pacman -S --needed --noconfirm docker-compose\n", true
	case "debian", "ubuntu", "raspbian":
		compose := "apt-get install -y -qq docker-compose-v2 || apt-get install -y -qq docker-compose\n"
		return apt + "apt-get install -y -qq docker.io\n" + compose, apt + compose, true
	case "fedora":
		return "dnf install -y moby-engine docker-compose\n", "dnf install -y docker-compose\n", true
	}
	return "", "", false
}

// BootstrapPlan is what a probed machine needs to run stacks. An empty plan
// means it is ready. It changes nothing.
func BootstrapPlan(r *Report, ingress string) ([]Step, error) {
	if r.Kernel != "Linux" {
		return nil, fmt.Errorf("NepFleet manages Linux hosts; this is %s", r.Kernel)
	}
	if !networkRe.MatchString(ingress) {
		return nil, fmt.Errorf("%q is not a network name", ingress)
	}
	var steps []Step
	dockerCmd, composeCmd, known := dockerInstall(r.OSID)
	switch {
	case !r.Docker:
		if !known {
			return nil, fmt.Errorf("installing Docker on %q isn't supported yet: install Docker and the Compose plugin yourself, then run this again", r.OSID)
		}
		steps = append(steps, Step{Text: "install Docker and the Compose plugin from the distribution's packages", Root: true, Script: dockerCmd})
	case r.Compose == "" && r.DockerRunning:
		if !known {
			return nil, fmt.Errorf("installing the Compose plugin on %q isn't supported yet: install it yourself, then run this again", r.OSID)
		}
		steps = append(steps, Step{Text: "install the Compose plugin from the distribution's packages", Root: true, Script: composeCmd})
	}
	if !r.DockerRunning {
		steps = append(steps, Step{Text: "start Docker, now and at boot", Root: true,
			Script: "if command -v systemctl >/dev/null 2>&1; then systemctl enable --now docker; fi\n"})
		if !r.Root {
			if !unixRe.MatchString(r.User) {
				return nil, fmt.Errorf("%q is not a user name this can add to a group", r.User)
			}
			steps = append(steps, Step{Text: "add " + r.User + " to the docker group, which is effectively root on this host", Root: true,
				Script: "usermod -aG docker " + r.User + "\n"})
		}
	}
	// Root even with the group: a group just added only counts from the next login.
	steps = append(steps, Step{Text: "create the ingress network " + ingress + ", if it is missing", Root: !r.DockerRunning || r.Root,
		Script: "docker network inspect " + ingress + " >/dev/null 2>&1 || docker network create " + ingress + "\n"})
	if slices.ContainsFunc(steps, func(s Step) bool { return s.Root }) && !r.Root && !r.Sudo && !r.SudoPassword {
		return steps, errors.New("this needs root: log in as root, or as a user who may use sudo")
	}
	return steps, nil
}

// heredoc is the marker around a root script; scripts are fixed and never hold it.
const heredoc = "NEPFLEET_BOOTSTRAP_EOF"

// NeedsSudoPassword says whether running steps asks for the sudo password: a
// root step, as a user whose sudo asks for one.
func NeedsSudoPassword(r *Report, steps []Step) bool {
	return !r.Root && !r.Sudo && r.SudoPassword && slices.ContainsFunc(steps, func(s Step) bool { return s.Root })
}

var (
	// ErrSudoPassword means sudo refused the password given.
	ErrSudoPassword = errors.New("sudo refused the password: check it and try again")
	// ErrNoSudo means the user may not use sudo on that machine.
	ErrNoSudo = errors.New("this user may not use sudo there: log in as a user who may, or as root")
	// ErrPasswordNeeded means a root step needs the sudo password, and none was given.
	ErrPasswordNeeded = errors.New("sudo asks for a password there: give it to run the root steps")
)

// shQuote quotes s for a POSIX shell.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// stepScript is what goes to the remote shell. errexit: a failed command stops
// the step instead of carrying on. A sudo password goes to sudo on its stdin
// through printf, a shell builtin, so it shows in no process list; the script
// itself travels on SSH's stdin, never as an argument.
func stepScript(s Step, isRoot bool, password string) string {
	body := "set -e\n" + s.Script
	switch {
	case !s.Root || isRoot:
		return body
	case password == "":
		return "sudo -n sh <<'" + heredoc + "'\n" + body + heredoc + "\n"
	}
	return "printf '%s\\n' " + shQuote(password) + " | sudo -S -p '' sh -c " + shQuote(body) + "\n"
}

// sudoError explains what sudo refused, and keeps the password out of the error.
func sudoError(err error, password string) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "incorrect password"), strings.Contains(msg, "Sorry, try again"):
		return ErrSudoPassword
	case strings.Contains(msg, "not in the sudoers file"), strings.Contains(msg, "is not allowed to"):
		return ErrNoSudo
	case strings.Contains(msg, "a password is required"), strings.Contains(msg, "a terminal is required"):
		return ErrPasswordNeeded
	case password != "" && strings.Contains(msg, password):
		return errors.New(strings.ReplaceAll(msg, password, "••••••"))
	}
	return err
}

// RunBootstrap runs the steps in order and stops at the first failure. password
// is the sudo password for root steps, when sudo asks for one: it is used for
// these steps only, and kept out of output and errors.
func RunBootstrap(ctx context.Context, run func(context.Context, string) ([]byte, error), r *Report, steps []Step, w io.Writer, password string) error {
	if strings.ContainsAny(password, "\n\x00") {
		return errors.New("a sudo password can't contain a line break")
	}
	if password == "" && NeedsSudoPassword(r, steps) {
		return ErrPasswordNeeded
	}
	for i, s := range steps {
		fmt.Fprintf(w, "%d/%d %s\n", i+1, len(steps), s.Text)
		if _, err := run(ctx, stepScript(s, r.Root, password)); err != nil {
			return fmt.Errorf("step %d (%s) failed, and later steps did not run: %w", i+1, s.Text, sudoError(err, password))
		}
	}
	return nil
}

// HostFile is hosts/<name>.yaml for a bootstrapped machine.
func HostFile(modeline, name, address string) string {
	var b strings.Builder
	b.WriteString(modeline)
	fmt.Fprintf(&b, "apiVersion: nepfleet/v1alpha1\nkind: Host\nmetadata:\n  name: %s\nspec:\n  address: %s\n", name, address)
	return b.String()
}
