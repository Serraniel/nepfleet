// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/Serraniel/nepfleet/internal/doctor"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

// binfmtArches are the architectures a binfmt stack registers, from its ARCHES
// value, or the template's default.
func binfmtArches(s *fleet.Stack) []string {
	raw := "amd64,arm64"
	if v, ok := s.Spec.Values["ARCHES"]; ok && !v.IsRef() && v.Literal != "" {
		raw = v.Literal
	}
	var out []string
	for _, a := range strings.Split(raw, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// reregisterBinfmt starts this host's binfmt stacks again when their emulators
// are gone, as after a reboot: the binfmt container runs once and exits, so
// Docker doesn't start it at boot, while NepFleet itself does start. It only
// starts the existing container, so nothing changes that apply didn't make.
func reregisterBinfmt(ctx context.Context, f *fleet.Fleet, host string, env doctor.Env,
	start func(ctx context.Context, project string) error, log io.Writer) {
	stacks, err := f.Stacks()
	if err != nil {
		return
	}
	for _, s := range stacks {
		t := s.Spec.Template
		if t == nil || t.Name != "binfmt" || (s.Spec.Target != "local" && s.Spec.Target != host) {
			continue
		}
		var missing []string
		for _, a := range binfmtArches(s) {
			if !doctor.Emulates(env, a) {
				missing = append(missing, a)
			}
		}
		if len(missing) == 0 {
			continue
		}
		if err := start(ctx, s.Metadata.Name); err != nil {
			fmt.Fprintf(log, "binfmt: emulators for %s are missing (after a reboot?), and starting %s failed: %v\n", strings.Join(missing, ", "), s.Metadata.Name, err)
			continue
		}
		fmt.Fprintf(log, "binfmt: registered emulators for %s again, with %s\n", strings.Join(missing, ", "), s.Metadata.Name)
	}
}

// startProject starts a compose project's existing containers and waits for
// them to finish (binfmt exits once registered).
func startProject(ctx context.Context, project string) error {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project="+project).Output() //nolint:gosec // a checked stack name, as one argument
	if err != nil {
		return fmt.Errorf("docker ps: %w", err)
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return fmt.Errorf("no container: apply %s first", project)
	}
	args := append([]string{"start", "-a"}, ids...)
	if b, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil { //nolint:gosec // container IDs from docker
		return fmt.Errorf("docker start: %w: %s", err, strings.TrimSpace(string(b)))
	}
	return nil
}
