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
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/remote"
	"github.com/Serraniel/nepfleet/internal/secrets"
)

var hostNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?$`)

// cmdHost: nepfleet host bootstrap <address> — prepares a machine over SSH.
func cmdHost(args []string, stdout, stderr io.Writer) error {
	const usage = "usage: nepfleet host bootstrap ssh://[user@]host[:port] [--name NAME] [--yes] [--fleet DIR] | nepfleet host install ssh://address [--name NAME] [--yes] [--skip-dir] | nepfleet host apply NAME [ssh://address] [--yes] [--fleet DIR]"
	if len(args) > 0 && args[0] == "apply" {
		return cmdHostApply(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "install" {
		return cmdHostInstall(args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "bootstrap" {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("host bootstrap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	yes := fs.Bool("yes", false, "run the plan without asking")
	sudoStdin := fs.Bool("sudo-password-stdin", false, "read the sudo password from the first line of stdin, for scripts (with --yes)")
	name := fs.String("name", "", "the host's name in the fleet (default: from its address)")
	fleetDir := fs.String("fleet", envOr("NEPFLEET_FLEET", "."), "fleet repository to register the host in, and take the ingress network from")
	config := fs.String("ssh-config", "", "ssh config file instead of ~/.ssh/config")
	pos, err := parseInterspersed(fs, args[1:])
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
	// Without a fleet here, it still prepares the machine, with the default network.
	ingress := "nepfleet_ingress"
	f, ferr := fleet.Load(*fleetDir)
	if ferr == nil && f.Spec.IngressNetwork != "" {
		ingress = f.Spec.IngressNetwork
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	ssh := remote.SSH{ConfigFile: *config}
	r, err := remote.Probe(ctx, ssh.Run, t)
	if err != nil {
		return err
	}
	printReport(stdout, r)
	steps, err := remote.BootstrapPlan(r, ingress)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "\nPlan for %s:\n", t)
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
	// What it says now counts, not what was planned.
	after, err := remote.Probe(ctx, ssh.Run, t)
	if err != nil {
		return fmt.Errorf("checking the result: %w", err)
	}
	if !after.Docker || after.Compose == "" {
		return errors.New("the steps ran, but Docker or the Compose plugin still isn't there")
	}
	if !after.DockerRunning {
		fmt.Fprintf(stdout, "⚠ this user can't reach Docker yet: a new group only counts from the next login\n")
	}
	if ferr != nil {
		fmt.Fprintf(stdout, "%s is ready. No fleet repository here, so it isn't registered: run this from one, or use --fleet\n", t)
		return nil
	}
	path := filepath.Join(f.Dir, "hosts", *name+".yaml")
	if _, err := os.Stat(path); err == nil {
		fmt.Fprintf(stdout, "%s is ready; %s exists and stays as it is\n", t, relTo(f.Dir, path))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(remote.HostFile(fleet.Modeline("host.schema.json"), *name, t.Host)), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s is ready, and registered in %s; commit it\n", t, relTo(f.Dir, path))
	return nil
}

// cmdHostApply: nepfleet host apply <name> — makes a host's mounts match its
// file in hosts/, over SSH, showing the plan first.
func cmdHostApply(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("host apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	yes := fs.Bool("yes", false, "run the plan without asking")
	sudoStdin := fs.Bool("sudo-password-stdin", false, "read the sudo password from the first line of stdin, for scripts (with --yes)")
	fleetDir := fs.String("fleet", envOr("NEPFLEET_FLEET", "."), "fleet repository")
	config := fs.String("ssh-config", "", "ssh config file instead of ~/.ssh/config")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 || len(pos) > 2 {
		return errors.New("usage: nepfleet host apply NAME [ssh://address] [--yes] [--fleet DIR]")
	}
	f, err := fleet.Load(*fleetDir)
	if err != nil {
		return err
	}
	h, err := f.LoadHost(pos[0])
	if err != nil {
		return err
	}
	address := h.Spec.Address
	if len(pos) == 2 {
		address = pos[1]
	}
	t, err := remote.ParseTarget(address)
	if err != nil {
		return err
	}
	creds := map[string]string{}
	var sec map[string]string
	for _, m := range h.Spec.Mounts {
		if m.PasswordSecret == "" {
			continue
		}
		if sec == nil {
			store := secrets.SOPS{Recipients: f.Spec.Secrets.Age}
			if sec, err = store.Load(filepath.Join(f.Dir, fleet.FleetSecretsFile)); err != nil {
				return err
			}
		}
		pw, ok := sec[m.PasswordSecret]
		if !ok {
			return fmt.Errorf("mount %s: %s is not in %s (nepfleet secret set --global %s)", m.Name, m.PasswordSecret, fleet.FleetSecretsFile, m.PasswordSecret)
		}
		creds[m.Name] = pw
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	ssh := remote.SSH{ConfigFile: *config}
	r, err := remote.Probe(ctx, ssh.Run, t)
	if err != nil {
		return err
	}
	run := func(ctx context.Context, script string) ([]byte, error) { return ssh.Run(ctx, t, script) }
	listed, err := run(ctx, remote.MountsPresent)
	if err != nil {
		return err
	}
	steps, err := remote.MountSteps(r.OSID, h.Spec.Mounts, creds, strings.Split(strings.TrimSpace(string(listed)), "\n"))
	if err != nil {
		return err
	}
	if !r.Root && !r.Sudo && !r.SudoPassword && len(steps) > 0 {
		return errors.New("this needs root: log in as root, or as a user who may use sudo")
	}
	if len(steps) == 0 {
		fmt.Fprintf(stdout, "%s has no mounts to set up\n", h.Metadata.Name)
		return nil
	}
	fmt.Fprintf(stdout, "Plan for %s:\n", t)
	for i, s := range steps {
		fmt.Fprintf(stdout, "  %d. %s (as root)\n", i+1, s.Text)
	}
	if !*yes && !ask(stdout, "Run this plan? [y/N] ", "y") {
		return errors.New("nothing changed; run again with --yes to go ahead")
	}
	pw, err := sudoPassword(r, steps, t, *sudoStdin, stdout)
	if err != nil {
		return err
	}
	if err := remote.RunBootstrap(ctx, run, r, steps, stdout, pw); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s's mounts match hosts/%s.yaml; each mounts on first use\n", h.Metadata.Name, h.Metadata.Name)
	return nil
}
