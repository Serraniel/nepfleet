// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/egress"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/render"
)

// sidecarLogs reads what a stack's egress sidecars have logged on this host.
func sidecarLogs(ctx context.Context, stack string) (string, int, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a", "--format", `{{.Names}}|{{.Label "com.docker.compose.service"}}`, //nolint:gosec // a stack's checked name, as one argument
		"--filter", "label=com.docker.compose.project="+stack).Output()
	if err != nil {
		return "", 0, fmt.Errorf("docker ps: %w", err)
	}
	var b strings.Builder
	found := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, service, ok := strings.Cut(line, "|")
		if !ok || !strings.HasSuffix(service, "-egress") {
			continue
		}
		found++
		// tcpdump's lines arrive on stdout, and its notes on stderr.
		logs, err := exec.CommandContext(ctx, "docker", "logs", name).CombinedOutput() //nolint:gosec // a name Docker gave
		if err != nil {
			return "", found, fmt.Errorf("docker logs %s: %w", name, err)
		}
		b.Write(logs)
		b.WriteByte('\n')
	}
	return b.String(), found, nil
}

// printSuggestion writes what to review, as YAML to paste where it can.
func printSuggestion(w io.Writer, stack string, r egress.Report) {
	if len(r.Hosts)+len(r.Addresses) == 0 {
		fmt.Fprintf(w, "# %s connected to nothing that isn't already allowed\n", stack)
	} else {
		fmt.Fprintf(w, "# stacks/%s/stack.yaml: review each one; this is what it did, not what it should do\n", stack)
		fmt.Fprintln(w, "spec:\n  egress:\n    allow:")
		for _, h := range r.Hosts {
			fmt.Fprintf(w, "      - %s\n", h)
		}
		for _, a := range r.Addresses {
			fmt.Fprintf(w, "      - %s   # no name seen for this; an address may change\n", a)
		}
	}
	if len(r.LookedUp) > 0 {
		fmt.Fprintln(w, "# looked up, but never connected to, so not suggested:")
		for _, n := range r.LookedUp {
			fmt.Fprintf(w, "#   %s\n", n)
		}
	}
}

// cmdEgress: nepfleet egress suggest <stack>. Runs where the stack runs.
func cmdEgress(args []string, stdout, stderr io.Writer) error {
	const usage = "usage: nepfleet egress suggest <stack>"
	if len(args) == 0 || args[0] != "suggest" {
		return errors.New(usage)
	}
	var c common
	fs := flag.NewFlagSet("egress suggest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New(usage)
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	s, err := e.fleet.LoadStack(pos[0])
	if err != nil {
		return err
	}
	mode, allow := fleet.EffectiveEgress(e.fleet, s)
	if mode != fleet.EgressLog {
		fmt.Fprintf(stderr, "note: %s's egress mode is %s; only log records new connections\n", s.Metadata.Name, mode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	logs, found, err := sidecarLogs(ctx, s.Metadata.Name)
	if err != nil {
		return err
	}
	if found == 0 {
		return fmt.Errorf("%s has no egress sidecars on this host: set spec.egress.mode: log, apply it, and let it run for a while", s.Metadata.Name)
	}
	known := slices.Clone(allow)
	if p, err := e.prepare(s); err == nil && p.tpl != nil {
		known = render.EgressAllow(known, p.tpl, render.EnvValues(p.out.Files[render.EnvFile]))
	}
	printSuggestion(stdout, s.Metadata.Name, egress.Parse(logs).Without(known))
	return nil
}
