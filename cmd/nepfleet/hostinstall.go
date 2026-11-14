// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Serraniel/nepfleet"
	"github.com/Serraniel/nepfleet/internal/remote"
)

// cmdHostInstall: nepfleet host install <address> — installs NepFleet as a
// container on a machine over SSH, showing the plan first.
func cmdHostInstall(args []string, stdout, stderr io.Writer) error {
	const usage = "usage: nepfleet host install ssh://[user@]host[:port] [--name NAME] [--yes] [--skip-dir]"
	fs := flag.NewFlagSet("host install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	yes := fs.Bool("yes", false, "run the plan without asking")
	sudoStdin := fs.Bool("sudo-password-stdin", false, "read the sudo password from the first line of stdin, for scripts (with --yes)")
	name := fs.String("name", "", "the host's name in its fleet (default: from its address)")
	skipDir := fs.Bool("skip-dir", false, remote.InstallDir+" exists already, owned by the SSH user: no root needed")
	config := fs.String("ssh-config", "", "ssh config file instead of ~/.ssh/config")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New(usage)
	}
	t, err := remote.ParseTarget(pos[0])
	if err != nil {
		return err
	}
	if *name == "" {
		*name = strings.ToLower(strings.NewReplacer(".", "-").Replace(t.Host))
	}
	if !hostNameRe.MatchString(*name) {
		return fmt.Errorf("%q cannot be a host's name; choose one with --name", *name)
	}
	compose, err := remote.PinCompose(nepfleet.Compose, version)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	ssh := remote.SSH{ConfigFile: *config}
	r, stopAgent, err := probeUnlocking(ctx, ssh, t, stdout)
	defer stopAgent()
	if err != nil {
		return err
	}
	printReport(stdout, r)
	steps, err := remote.InstallPlan(r, *name, compose, *skipDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "\nPlan for %s, installing NepFleet in %s as host %s:\n", t, remote.InstallDir, *name)
	for i, s := range steps {
		root := ""
		if s.Root {
			root = " (as root)"
		}
		fmt.Fprintf(stdout, "  %d. %s%s\n", i+1, s.Text, root)
	}
	if !*yes && !ask(stdout, "Run this plan? [y/N] ", "y") {
		return errors.New("nothing changed; run again with --yes to go ahead")
	}
	run := func(ctx context.Context, script string) ([]byte, error) { return ssh.Run(ctx, t, script) }
	pw, err := sudoPassword(r, steps, t, *sudoStdin, stdout)
	if err != nil {
		return err
	}
	if err := remote.RunBootstrap(ctx, run, r, steps, stdout, pw); err != nil {
		return err
	}
	after, err := remote.Probe(ctx, ssh.Run, t)
	if err != nil {
		return fmt.Errorf("checking the result: %w", err)
	}
	if !after.NepFleetRunning {
		return fmt.Errorf("the steps ran, but no NepFleet answers on %s yet: check with ssh, then docker compose -f %s/compose.yaml logs", t, remote.InstallDir)
	}
	fmt.Fprintf(stdout, `
NepFleet runs on %[1]s (%[2]s), with its fleet repository in %[3]s/fleet.
Next:
  1. Back up the age key, which never left the machine: without it, no secret
     in the fleet can be read. For example, copy it to a password manager:
       ssh %[1]s cat %[3]s/age.key
  2. Connect from here:   nepfleet remote add %[1]s --name %[4]s && nepfleet remote connect %[4]s
     (or on the Fleets page of the app)
  3. Push the fleet repository to a private remote, as its backup and history.
`, t, after.NepFleetAPI, remote.InstallDir, *name)
	return nil
}
