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

// DataSafety makes a rollback restore volume data too: before the new
// version starts, the stack's containers are stopped and their writable
// volumes copied (neptide's snapshotter: it skips read-only mounts, system
// paths and data other running containers use). If the new version fails,
// the data is put back before the previous version starts again.
type DataSafety struct {
	Snapshots *snapshot.Snapshotter
	Engine    *engine.Client
	// Exclude lists mount destinations never snapshotted (large media).
	Exclude []string
	// ExcludeVolumes lists volume names (as compose names them,
	// <project>_<volume>) never snapshotted.
	ExcludeVolumes []string
	// Log receives notes such as skipped mounts (may be nil).
	Log func(msg string)
}

// guard is the state of one guarded apply.
type guard struct {
	d     *DataSafety
	snaps []*snapshot.Snapshot
}

// take stops the running containers of stack and snapshots them. On error
// the containers are started again and nothing has changed.
func (d *DataSafety) take(ctx context.Context, r Runner, stack, dir string) (*guard, error) {
	out, err := r.Run(ctx, dir, compose(stack, dir, "ps", "--quiet", "--status", "running")...)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(out))
	g := &guard{d: d}
	if len(ids) == 0 {
		return g, nil // nothing runs: no data in use to protect
	}
	// Before stopping anything: the helper pull must not add downtime.
	if err := d.Snapshots.Prepare(ctx); err != nil {
		return nil, fmt.Errorf("snapshot helper: %w", err)
	}
	if _, err := r.Run(ctx, dir, compose(stack, dir, "stop")...); err != nil {
		return nil, fmt.Errorf("stop for the snapshot: %w", err)
	}
	for _, id := range ids {
		ct, err := d.Engine.Inspect(ctx, id)
		if err == nil {
			exclude := append([]string(nil), d.Exclude...)
			for _, m := range ct.Mounts {
				if m.Type == "volume" && slices.Contains(d.ExcludeVolumes, m.Name) {
					exclude = append(exclude, m.Destination)
				}
			}
			var s *snapshot.Snapshot
			if s, err = d.Snapshots.Take(ctx, ct, exclude); err == nil {
				g.snaps = append(g.snaps, s)
				if d.Log != nil {
					for _, sk := range s.Skipped {
						d.Log("snapshot of " + strings.TrimPrefix(ct.Name, "/") + " skips " + sk)
					}
				}
			}
		}
		if err != nil {
			_, serr := r.Run(context.WithoutCancel(ctx), dir, compose(stack, dir, "start")...)
			return nil, errors.Join(fmt.Errorf("snapshot before apply (nothing changed; free space, exclude large volumes with snapshot: false / snapshotExclude, or apply --no-snapshot to go ahead without data rollback): %w", err), serr)
		}
	}
	return g, nil
}

// restore stops whatever runs now and puts the snapshots back. It must
// succeed before the previous version may start on the data.
func (g *guard) restore(ctx context.Context, r Runner, stack, dir string) error {
	if g == nil || len(g.snaps) == 0 {
		return nil
	}
	timeout := 10 * time.Minute
	for _, s := range g.snaps {
		timeout = max(timeout, s.RestoreTimeout())
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if _, err := r.Run(rctx, dir, compose(stack, dir, "stop")...); err != nil {
		return fmt.Errorf("stop the failed version: %w", err)
	}
	for _, s := range g.snaps {
		if err := g.d.Snapshots.Restore(rctx, s); err != nil {
			return fmt.Errorf("restore %s: %w", s.Container, err)
		}
	}
	return nil
}
