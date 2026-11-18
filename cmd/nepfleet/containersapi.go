// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

// containersAPI is GET /api/v1/stacks/{name}/containers.
func containersAPI(ctx context.Context, c common, stack string) ([]api.Container, error) {
	out, err := withStack(c, stack, func(_ *env, s *fleet.Stack) (any, error) {
		return containersAPIFor(ctx, stack, s.Spec.Mode == "swarm")
	})
	if err != nil {
		return nil, err
	}
	return out.([]api.Container), nil
}

// composePS is one line of docker compose ps --format json.
type composePS struct {
	ID, Name, Service, State, Health, Status, Image string
}

func stackComposeContainers(ctx context.Context, stack string) ([]api.Container, error) {
	raw, err := output(ctx, "compose", "--project-name", stack, "ps", "--all", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("listing %s's containers: %w", stack, err)
	}
	rows, err := parseComposePS(raw)
	if err != nil {
		return nil, fmt.Errorf("listing %s's containers: %w", stack, err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	restarts := map[string]int{}
	if inspect, err := output(ctx, append([]string{"inspect", "--format", "{{.Id}} {{.RestartCount}}"}, ids...)...); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(inspect), "\n") {
			if id, n, ok := strings.Cut(line, " "); ok {
				restarts[id], _ = strconv.Atoi(n)
			}
		}
	}
	out := make([]api.Container, 0, len(rows))
	for _, r := range rows {
		n := restarts[r.ID]
		for id, count := range restarts { // inspect gives full ids, ps short ones
			if strings.HasPrefix(id, r.ID) {
				n = count
			}
		}
		out = append(out, api.Container{Service: r.Service, Name: r.Name, State: r.State, Health: r.Health,
			Status: r.Status, Image: r.Image, Restarts: n})
	}
	slices.SortFunc(out, func(a, b api.Container) int { return strings.Compare(a.Service+a.Name, b.Service+b.Name) })
	return out, nil
}

// parseComposePS reads compose's JSON: one object per line (Compose 2.21
// and later) or one array (earlier).
func parseComposePS(raw string) ([]composePS, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var rows []composePS
		err := json.Unmarshal([]byte(raw), &rows)
		return rows, err
	}
	var rows []composePS
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r composePS
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, sc.Err()
}

// stackPS is one line of docker stack ps --format '{{json .}}'.
type stackPS struct {
	Name, Image, Node, CurrentState, Error string
}

func swarmTasks(ctx context.Context, stack string) ([]api.Container, error) {
	raw, err := output(ctx, "stack", "ps", stack, "--filter", "desired-state=running", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		return nil, fmt.Errorf("listing %s's tasks: %w", stack, err)
	}
	return parseStackPS(stack, raw)
}

func parseStackPS(stack, raw string) ([]api.Container, error) {
	var out []api.Container
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var t stackPS
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return nil, err
		}
		// "app_web.1" → service "web"; the image without its pinned digest.
		service := strings.TrimPrefix(t.Name, stack+"_")
		if i := strings.LastIndex(service, "."); i > 0 {
			service = service[:i]
		}
		image, _, _ := strings.Cut(t.Image, "@")
		state, _, _ := strings.Cut(t.CurrentState, " ")
		out = append(out, api.Container{Service: service, Name: t.Name, State: strings.ToLower(state),
			Status: t.CurrentState, Image: image, Node: t.Node, Error: t.Error})
	}
	return out, sc.Err()
}

// serviceActionAPI is POST /api/v1/stacks/{name}/services/{service}/{action}:
// not while an apply or restore of the stack runs.
func serviceActionAPI(ctx context.Context, c common, stack, service, action string) error {
	_, err := withStack(c, stack, func(e *env, s *fleet.Stack) (any, error) {
		release, err := lockStack(e.stateDir, stack)
		if err != nil {
			return nil, api.ErrBusy
		}
		defer release()
		swarm := s.Spec.Mode == "swarm"
		if swarm && action != api.ServiceRestart {
			return nil, api.ErrNotSupported
		}
		cs, err := containersAPIFor(ctx, stack, swarm)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(cs, func(c api.Container) bool { return c.Service == service }) {
			return nil, api.ErrNotFound
		}
		if swarm {
			// A forced update replaces the service's tasks, one by one.
			return nil, docker(ctx, nil, io.Discard, "service", "update", "--force", "--detach", "--quiet", stack+"_"+service)
		}
		return nil, docker(ctx, nil, io.Discard, "compose", "--project-name", stack, action, service)
	})
	return err
}

func containersAPIFor(ctx context.Context, stack string, swarm bool) ([]api.Container, error) {
	if swarm {
		return swarmTasks(ctx, stack)
	}
	return stackComposeContainers(ctx, stack)
}

// maxLogBytes bounds a logs answer: lines can be long.
const maxLogBytes = 1 << 20

// logsAPI is GET /api/v1/stacks/{name}/containers/{container}/logs: only a
// container of that stack, by the name the containers list gives.
// logArgs is the docker command line for a container's last lines (and,
// with follow, what comes after): only a container of that stack, by the
// name its containers list gives.
func logArgs(ctx context.Context, c common, stack, container string, lines int, follow bool) ([]string, error) {
	out, err := withStack(c, stack, func(_ *env, s *fleet.Stack) (any, error) {
		swarm := s.Spec.Mode == "swarm"
		cs, err := containersAPIFor(ctx, stack, swarm)
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(cs, func(c api.Container) bool { return c.Name == container })
		if i < 0 {
			return nil, api.ErrNotFound
		}
		args := []string{"logs", "--timestamps", "--tail", strconv.Itoa(lines)}
		if swarm {
			args = []string{"service", "logs", "--timestamps", "--no-task-ids", "--tail", strconv.Itoa(lines)}
		}
		if follow {
			args = append(args, "--follow")
		}
		if swarm {
			return append(args, stack+"_"+cs[i].Service), nil
		}
		return append(args, container), nil
	})
	if err != nil {
		return nil, err
	}
	return out.([]string), nil
}

// followLogsAPI is .../logs?follow=1: the last lines, then the container's
// output as it comes, until ctx ends.
func followLogsAPI(ctx context.Context, c common, stack, container string, lines int, w io.Writer) error {
	args, err := logArgs(ctx, c, stack, container, lines, true)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // names from docker's own listing
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("following %s's logs: %w", container, err)
	}
	return nil
}

func logsAPI(ctx context.Context, c common, stack, container string, lines int) (string, error) {
	args, err := logArgs(ctx, c, stack, container, lines, false)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // names from docker's own listing
	var buf limitedBuffer
	buf.max = maxLogBytes
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("reading %s's logs: %w", container, err)
	}
	return buf.String(), nil
}

// limitedBuffer keeps the first max bytes written and drops the rest,
// marking that it did.
type limitedBuffer struct {
	strings.Builder
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room < len(p) {
		if room > 0 {
			_, _ = b.Builder.Write(p[:room])
		}
		if !b.truncated {
			b.truncated = true
			_, _ = b.WriteString("\n… (cut at 1 MB)\n")
		}
		return len(p), nil
	}
	return b.Builder.Write(p)
}
