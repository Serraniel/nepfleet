// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Serraniel/neptide/pkg/engine"
	"github.com/Serraniel/neptide/pkg/snapshot"
)

// SnapshotEngine is what DataSafety needs from the Docker API.
type SnapshotEngine interface {
	AllContainers(ctx context.Context) ([]engine.ContainerSummary, error)
	Inspect(ctx context.Context, id string) (*engine.Container, error)
	Stop(ctx context.Context, id string, timeout time.Duration) error
	Start(ctx context.Context, id string) error
	InspectVolume(ctx context.Context, name string) (*engine.Volume, error)
	RemoveVolume(ctx context.Context, name string) error
}

// DataSafety makes a rollback restore volume data too: before the new
// version starts, the stack's containers are stopped and their writable
// volumes copied (neptide's snapshotter: it skips read-only mounts, system
// paths and data other running containers use). If the new version fails,
// the data is put back before the previous version starts again.
type DataSafety struct {
	Snapshots *snapshot.Snapshotter
	Engine    SnapshotEngine
	// Exclude lists mount destinations never snapshotted (large media).
	Exclude []string
	// ExcludeVolumes lists compose volume keys (as in the compose file's
	// volumes:) never snapshotted, matched by compose's volume label, so
	// name: overrides and external volumes are found too.
	ExcludeVolumes []string
	// Log receives notes such as skipped mounts (may be nil).
	Log func(msg string)
}

// guard is the state of one guarded apply.
type guard struct {
	d       *DataSafety
	stack   string
	running []string // container IDs that ran before and must run again on failure
	snaps   []*snapshot.Snapshot
}

// stop stops a container with its own grace period (compose writes
// stop_grace_period into StopTimeout), so a database shuts down cleanly
// and the snapshot is not a crash state.
func (d *DataSafety) stop(ctx context.Context, id string) error {
	timeout := 10 * time.Second // Docker's default
	if ct, err := d.Engine.Inspect(ctx, id); err == nil {
		if s, ok := ct.Config["StopTimeout"].(float64); ok && s > 0 {
			timeout = time.Duration(s) * time.Second
		}
	}
	return d.Engine.Stop(ctx, id, timeout)
}

// project lists every container of the compose project, running or not,
// orphans included.
func (d *DataSafety) project(ctx context.Context, stack string) ([]engine.ContainerSummary, error) {
	all, err := d.Engine.AllContainers(ctx)
	if err != nil {
		return nil, err
	}
	var out []engine.ContainerSummary
	for _, c := range all {
		// docker compose run containers are one-off commands, not the stack
		// (with --rm they vanish when stopped).
		if c.Labels["com.docker.compose.project"] == stack && c.Labels["com.docker.compose.oneoff"] != "True" {
			out = append(out, c)
		}
	}
	return out, nil
}

// take stops every container of stack and snapshots them all, a volume
// shared by several only once. On error the containers that ran are started
// again, the partial snapshots are discarded, and nothing has changed.
func (d *DataSafety) take(ctx context.Context, stack string) (*guard, error) {
	cts, err := d.project(ctx, stack)
	if err != nil {
		return nil, err
	}
	g := &guard{d: d, stack: stack}
	if len(cts) == 0 {
		return g, nil
	}
	// Before stopping anything: the helper pull must not add downtime.
	if err := d.Snapshots.Prepare(ctx); err != nil {
		return nil, fmt.Errorf("snapshot helper: %w", err)
	}
	fail := func(err error) (*guard, error) {
		rctx := context.WithoutCancel(ctx)
		g.discard(rctx)
		return nil, errors.Join(fmt.Errorf("snapshot before apply (nothing changed; free space, exclude large volumes with snapshot: false / snapshotExclude, or apply --no-snapshot to go ahead without data rollback): %w", err), g.startAgain(rctx))
	}
	for _, c := range cts {
		if c.State == "running" || c.State == "paused" || c.State == "restarting" {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			g.running = append(g.running, c.ID)
			// Not cancelled midway: the daemon would keep stopping anyway,
			// and a start sent meanwhile could be lost.
			if err := d.stop(context.WithoutCancel(ctx), c.ID); err != nil {
				return fail(fmt.Errorf("stop %s: %w", name(c), err))
			}
		}
	}
	seen := map[string]bool{} // volumes already copied through another container
	for _, c := range cts {
		ct, err := d.Engine.Inspect(ctx, c.ID)
		if err != nil {
			return fail(err)
		}
		exclude := append([]string(nil), d.Exclude...)
		for _, m := range ct.Mounts {
			if m.Type != "volume" {
				continue
			}
			if seen[m.Name] || d.excluded(ctx, stack, m.Name) {
				exclude = append(exclude, m.Destination)
			}
		}
		s, err := d.Snapshots.Take(ctx, ct, exclude)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", name(c), err))
		}
		g.snaps = append(g.snaps, s)
		for _, it := range s.Items {
			if it.Mount.Type == "volume" {
				seen[it.Mount.Name] = true
			}
		}
		if d.Log != nil {
			for _, sk := range s.Skipped {
				d.Log("snapshot of " + name(c) + " skips " + sk)
			}
		}
	}
	return g, nil
}

// holdsData says whether the snapshot has any data in it. With no containers to
// look at, or everything excluded, it has none, and a restore would change nothing.
func (g *guard) holdsData() bool {
	if g == nil {
		return false
	}
	return slices.ContainsFunc(g.snaps, func(s *snapshot.Snapshot) bool { return len(s.Items) > 0 })
}

// excluded reports a volume the template or stack keeps out of snapshots.
func (d *DataSafety) excluded(ctx context.Context, stack, vol string) bool {
	if len(d.ExcludeVolumes) == 0 {
		return false
	}
	v, err := d.Engine.InspectVolume(ctx, vol)
	if err != nil || v.Labels["com.docker.compose.project"] != stack {
		return false
	}
	return slices.Contains(d.ExcludeVolumes, v.Labels["com.docker.compose.volume"])
}

func name(c engine.ContainerSummary) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID
}

// startAgain starts the containers that ran before take.
func (g *guard) startAgain(ctx context.Context) error {
	var errs []error
	for _, id := range g.running {
		errs = append(errs, g.d.Engine.Start(ctx, id))
	}
	return errors.Join(errs...)
}

// stopAll stops every container of the project, also services the failed
// version added: nothing may write to the data while it is restored.
func (g *guard) stopAll(ctx context.Context) error {
	if g == nil || len(g.snaps) == 0 {
		return nil
	}
	cts, err := g.d.project(ctx, g.stack)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range cts {
		if c.State == "running" || c.State == "paused" || c.State == "restarting" {
			errs = append(errs, g.d.stop(ctx, c.ID))
		}
	}
	return errors.Join(errs...)
}

// restore puts the snapshots back; the stack must be stopped (stopAll).
func (g *guard) restore(ctx context.Context) error {
	if g == nil || len(g.snaps) == 0 {
		return nil
	}
	timeout := 10 * time.Minute
	for _, s := range g.snaps {
		timeout = max(timeout, s.RestoreTimeout())
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	for _, s := range g.snaps {
		if err := g.d.Snapshots.Restore(rctx, s); err != nil {
			return fmt.Errorf("restore %s: %w", s.Container, err)
		}
	}
	return nil
}

// discard removes this apply's snapshot volumes: after a healthy apply they
// are not needed, and they would hold a full copy of the data otherwise.
func (g *guard) discard(ctx context.Context) {
	if g == nil {
		return
	}
	for _, s := range g.snaps {
		for _, it := range s.Items {
			if err := g.d.Engine.RemoveVolume(ctx, it.Volume); err != nil && g.d.Log != nil {
				g.d.Log("could not remove snapshot volume " + it.Volume + ": " + err.Error())
			}
		}
	}
	g.snaps = nil
}
