// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Serraniel/neptide/pkg/snapshot"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

// archiveName is a docker-volume-backup archive as volume-backup names it.
var archiveName = regexp.MustCompile(`^backup-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}-[0-9]{2}-[0-9]{2}\.tar\.gz(\.age)?$`)

// archiveFolder is the host folder where the volume-backup stack of a stack's
// host keeps its archives.
func archiveFolder(e *env, s *fleet.Stack) (string, error) {
	here := func(target string) bool { return target == "local" || target == e.host }
	if !here(s.Spec.Target) {
		return "", api.InvalidError{Reason: fmt.Sprintf("%s runs on %s: restore it from the NepFleet there", s.Metadata.Name, s.Spec.Target)}
	}
	all, err := e.fleet.Stacks()
	if err != nil {
		return "", err
	}
	for _, b := range all {
		if t := b.Spec.Template; t == nil || t.Name != "volume-backup" || !here(b.Spec.Target) {
			continue
		}
		dir := cmp.Or(b.Spec.Values["ARCHIVE_PATH"].Literal, "/srv/backups")
		dir, err := e.fleet.ResolvePath(dir)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dir) || strings.ContainsAny(dir, ":,\n") {
			return "", api.InvalidError{Reason: "the backup folder " + dir + " is not an absolute host path"}
		}
		return filepath.Clean(dir), nil
	}
	return "", api.InvalidError{Reason: "no volume-backup stack runs on this host: enable backups first (nepfleet enable backups)"}
}

// listArchives lists the archives in a host folder, newest first, through a
// helper container: the server may run in one that doesn't mount it.
func listArchives(ctx context.Context, dir string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", "-v", dir+":/archive:ro", //nolint:gosec // a checked absolute path, no shell
		snapshot.HelperImage, "ls", "-1", "/archive").Output()
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", dir, cmdErr(err))
	}
	var names []string
	for _, n := range strings.Fields(string(out)) {
		if archiveName.MatchString(n) {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	slices.Reverse(names) // the name holds the time
	return names, nil
}

// openArchive reads an archive through a helper container, and through age
// when it's encrypted.
func openArchive(dir, name, identity string) (io.ReadCloser, error) {
	cat := exec.Command("docker", "run", "--rm", "--network", "none", "-v", dir+":/archive:ro", //nolint:gosec // checked path and name, no shell
		snapshot.HelperImage, "cat", "/archive/"+name)
	raw, err := cat.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cat.Start(); err != nil {
		return nil, err
	}
	if !strings.HasSuffix(name, ".age") {
		return &cmdReader{ReadCloser: raw, cmd: cat}, nil
	}
	age := exec.Command("age", "--decrypt", "--identity", identity) //nolint:gosec // the key file sops uses
	age.Stdin = raw
	out, err := age.StdoutPipe()
	if err != nil {
		_ = cat.Process.Kill()
		return nil, err
	}
	if err := age.Start(); err != nil {
		_ = cat.Process.Kill()
		return nil, fmt.Errorf("age: %w", err)
	}
	return &chainReader{ReadCloser: out, cmds: []*exec.Cmd{age, cat}}, nil
}

// chainReader waits for every command of a pipeline when closed.
type chainReader struct {
	io.ReadCloser
	cmds []*exec.Cmd
}

func (r *chainReader) Close() error {
	_ = r.ReadCloser.Close()
	var errs []error
	for _, c := range r.cmds {
		if err := c.Wait(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// backupsAPI is GET /api/v1/stacks/{name}/backups.
func backupsAPI(ctx context.Context, c common, stack string) (api.Backups, error) {
	e, err := c.open()
	if err != nil {
		return api.Backups{}, err
	}
	s, err := e.fleet.LoadStack(stack)
	if err != nil {
		return api.Backups{}, api.ErrNotFound
	}
	dir, err := archiveFolder(e, s)
	if err != nil {
		return api.Backups{}, err
	}
	names, err := listArchives(ctx, dir)
	if err != nil {
		return api.Backups{}, err
	}
	own, err := projectVolumes(s.Metadata.Name)
	if err != nil {
		return api.Backups{}, err
	}
	keys := slices.Sorted(func(yield func(string) bool) {
		for _, k := range own {
			if !yield(k) {
				return
			}
		}
	})
	return api.Backups{Folder: dir, Archives: append([]string{}, names...), Volumes: slices.Compact(keys)}, nil
}

// startRestore is POST /api/v1/stacks/{name}/restore: the restore as a job.
func startRestore(ctx context.Context, c common, stack, archive string, only []string, by string) (func(io.Writer) error, error) {
	if !archiveName.MatchString(archive) {
		return nil, api.InvalidError{Reason: archive + " is not a backup archive name"}
	}
	if !apiMu.TryLock() {
		return nil, api.ErrBusy
	}
	handedOver := false
	defer func() {
		if !handedOver {
			apiMu.Unlock()
		}
	}()
	e, err := c.open()
	if err != nil {
		return nil, err
	}
	s, err := e.fleet.LoadStack(stack)
	if err != nil {
		return nil, api.ErrNotFound
	}
	dir, err := archiveFolder(e, s)
	if err != nil {
		return nil, err
	}
	handedOver = true
	return func(log io.Writer) error {
		defer apiMu.Unlock()
		fmt.Fprintf(log, "restore %s from %s, requested by %s over the API\n", stack, archive, by)
		open := func() (io.ReadCloser, error) { return openArchive(dir, archive, ageKeyFile()) }
		// The client confirmed with the stack's name before asking.
		return restoreVolumes(ctx, e, s, archive, open, only, func() error { return nil }, log)
	}, nil
}
