// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

// projectRunning says whether a stack has running containers on this host.
func projectRunning(ctx context.Context, stack string) (bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-q", "--filter", "status=running", //nolint:gosec // a stack's checked name, as one argument
		"--filter", "label=com.docker.compose.project="+stack).Output()
	if err != nil {
		return false, fmt.Errorf("docker ps: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// adoptStack records a running stack as applied, for a new NepFleet installation
// taking over from an old one. It starts and stops nothing.
func (e *env) adoptStack(ctx context.Context, s *fleet.Stack, stdout io.Writer) error {
	name := s.Metadata.Name
	release, err := lockStack(e.stateDir, name)
	if err != nil {
		return err
	}
	defer release()
	p, err := e.prepare(s)
	if err != nil {
		return err
	}
	if !p.plan.First {
		fmt.Fprintf(stdout, "%s: already managed here\n", name)
		return nil
	}
	// A secret made up now can't be what the running containers use.
	if len(p.out.Generated) > 0 {
		return fmt.Errorf("%s: this would generate new secrets, so the fleet repository isn't what's running: commit its secrets first", name)
	}
	running, err := projectRunning(ctx, name)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("%s isn't running on this host: nothing to take over, so use nepfleet apply", name)
	}
	if err := deploy.Adopt(p.plan.Dir, p.out); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if p.tpl != nil {
		if err := deploy.RecordVersion(p.plan.Dir, p.tpl.Metadata.Name, p.tpl.Spec.Version); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	fmt.Fprintf(stdout, "%s: adopted; what runs now counts as applied\n", name)
	return nil
}

// cmdAdopt: nepfleet adopt [stack...]
func cmdAdopt(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("adopt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	stacks, err := e.stacks(fs.Args())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var errs []error
	for _, s := range stacks {
		if err := e.adoptStack(ctx, s, stdout); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		fmt.Fprintln(stdout, "Check with nepfleet plan: it should show no changes. If it does, what runs isn't what the fleet repository says.")
	}
	return errors.Join(errs...)
}
