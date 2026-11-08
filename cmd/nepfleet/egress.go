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

	"github.com/Serraniel/nepfleet/internal/doctor"
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

// printSuggestion writes what to review, as YAML to paste where it can. In mode
// enforce, what's logged was refused.
func printSuggestion(w io.Writer, stack string, r egress.Report, denied bool) {
	switch {
	case len(r.Hosts)+len(r.Addresses) == 0 && denied:
		fmt.Fprintf(w, "# nothing of %s's was refused, as far as the logs go back\n", stack)
	case len(r.Hosts)+len(r.Addresses) == 0:
		fmt.Fprintf(w, "# %s connected to nothing that isn't already allowed\n", stack)
	default:
		if denied {
			fmt.Fprintf(w, "# refused for %s (at most 20 a minute are logged); allow only what it needs, in stacks/%s/stack.yaml:\n", stack, stack)
		} else {
			fmt.Fprintf(w, "# stacks/%s/stack.yaml: review each one; this is what it did, not what it should do\n", stack)
		}
		fmt.Fprintln(w, "spec:\n  egress:\n    allow:")
		for _, h := range r.Hosts {
			fmt.Fprintf(w, "      - %s\n", h)
		}
		for _, a := range r.Addresses {
			fmt.Fprintf(w, "      - %s   # no name seen for this; an address may change\n", a)
		}
	}
	if len(r.LookedUp) > 0 && !denied {
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
	if mode != fleet.EgressLog && mode != fleet.EgressEnforce {
		fmt.Fprintf(stderr, "note: %s's egress mode is %s; log records connections, and enforce what it refuses\n", s.Metadata.Name, mode)
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
	printSuggestion(stdout, s.Metadata.Name, egress.Parse(logs).Without(known), mode == fleet.EgressEnforce)
	return nil
}

// egressCheck is doctor's line on which stacks have outbound traffic limited or
// recorded; false when none do.
func egressCheck(f *fleet.Fleet, stacks []*fleet.Stack) (doctor.Check, bool) {
	var enforce, logged []string
	for _, s := range stacks {
		switch mode, _ := fleet.EffectiveEgress(f, s); mode {
		case fleet.EgressEnforce:
			enforce = append(enforce, s.Metadata.Name)
		case fleet.EgressLog:
			logged = append(logged, s.Metadata.Name)
		}
	}
	if len(enforce)+len(logged) == 0 {
		return doctor.Check{}, false
	}
	var parts []string
	if len(enforce) > 0 {
		parts = append(parts, "enforced: "+strings.Join(enforce, ", "))
	}
	if len(logged) > 0 {
		parts = append(parts, "recorded: "+strings.Join(logged, ", "))
	}
	c := doctor.Check{Name: "Egress", Status: doctor.OK, Detail: strings.Join(parts, "; ")}
	if len(logged) > 0 {
		c.Fix = "nepfleet egress suggest <stack> turns what was recorded into an allow list, for mode enforce"
	}
	return c, true
}
