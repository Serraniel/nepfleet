// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Stack states for the API, from this host's containers.
const (
	stateRunning   = "running"   // every container runs (one-shot ones finished fine)
	stateUnhealthy = "unhealthy" // one reports unhealthy
	statePartial   = "partial"   // some are down
	stateStopped   = "stopped"   // containers exist, none runs
)

// container is one line of docker ps: its compose project, state and status.
type container struct{ project, state, status string }

// composeContainers lists this host's compose containers, in one docker call.
func composeContainers(ctx context.Context) ([]container, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a", "--filter", "label=com.docker.compose.project",
		"--format", `{{.Label "com.docker.compose.project"}}`+"\t{{.State}}\t{{.Status}}").Output()
	if err != nil {
		return nil, err
	}
	var cs []container
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.SplitN(line, "\t", 3); len(f) == 3 {
			cs = append(cs, container{f[0], f[1], f[2]})
		}
	}
	return cs, nil
}

// stackStates sums containers up per project. A project without containers has
// no state: not applied, or on another host.
func stackStates(cs []container) map[string]string {
	type count struct{ total, running, done, unhealthy int }
	per := map[string]*count{}
	for _, c := range cs {
		n := per[c.project]
		if n == nil {
			n = &count{}
			per[c.project] = n
		}
		n.total++
		switch {
		case c.state == "running" && strings.Contains(c.status, "(unhealthy)"):
			n.running++
			n.unhealthy++
		case c.state == "running":
			n.running++
		case c.state == "exited" && strings.HasPrefix(c.status, "Exited (0)"):
			n.done++ // a one-shot setup or migration that finished
		}
	}
	out := make(map[string]string, len(per))
	for p, n := range per {
		switch {
		case n.unhealthy > 0:
			out[p] = stateUnhealthy
		case n.running+n.done == n.total && n.running > 0:
			out[p] = stateRunning
		case n.running == 0:
			out[p] = stateStopped
		default:
			out[p] = statePartial
		}
	}
	return out
}
