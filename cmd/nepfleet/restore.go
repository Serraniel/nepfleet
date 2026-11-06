// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"archive/tar"
	"cmp"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Serraniel/neptide/pkg/snapshot"
)

// volumeName is a Docker volume name as compose creates them.
var volumeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// cmdRestore puts a stack's volumes back from a volume-backup archive
// (offen/docker-volume-backup, age-encrypted to the fleet's keys). The
// current data of each volume is copied to <volume>-before-restore-<time>
// first, so a restore never loses anything.
func cmdRestore(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	archive := fs.String("archive", "", "backup archive (backup-….tar.gz.age, or .tar.gz)")
	identity := fs.String("identity", "", "age identity file (default: the key sops uses)")
	only := fs.String("volume", "", "restore only this volume (comma separated volume keys, e.g. db,data)")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || *archive == "" {
		return errors.New("usage: nepfleet restore <stack> --archive FILE [--volume KEY,…] [--yes]")
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	s, err := e.fleet.LoadStack(pos[0])
	if err != nil {
		return err
	}
	if s.Spec.Mode == "swarm" {
		return errors.New("restore supports compose stacks; for a swarm stack, scale it to 0 and restore its volumes on the node that holds them")
	}
	key := cmp.Or(*identity, ageKeyFile())
	open := func() (io.ReadCloser, error) { return decrypt(*archive, key) }

	// What the archive has for this stack, read before anything changes.
	rc, err := open()
	if err != nil {
		return err
	}
	inArchive, err := archiveVolumes(rc)
	_ = rc.Close()
	if err != nil {
		return fmt.Errorf("reading %s: %w", *archive, err)
	}
	// The stack's volumes are the ones compose labelled with its project,
	// not a name prefix: stack "app" must never touch stack "app_x".
	own, err := projectVolumes(s.Metadata.Name)
	if err != nil {
		return err
	}
	if len(own) == 0 {
		return fmt.Errorf("%s has no volumes on this host: apply it here first", s.Metadata.Name)
	}
	var vols []string
	for _, v := range inArchive {
		key, ok := own[v]
		if !ok {
			continue
		}
		if *only != "" && !slices.Contains(strings.Split(*only, ","), key) {
			continue
		}
		if !volumeName.MatchString(v) {
			return fmt.Errorf("%q is not a volume name", v)
		}
		vols = append(vols, v)
	}
	if len(vols) == 0 {
		return fmt.Errorf("the archive has no volumes of %s (it has: %s)", s.Metadata.Name, strings.Join(inArchive, ", "))
	}

	stamp := time.Now().Format("20060102-150405")
	fmt.Fprintf(stdout, "Restore %s from %s:\n", s.Metadata.Name, *archive)
	for _, v := range vols {
		fmt.Fprintf(stdout, "  %s (current data kept in %s-before-restore-%s)\n", v, v, stamp)
	}
	if !*yes && !ask(stdout, fmt.Sprintf("The stack stops while its data is replaced. Type %s to continue: ", s.Metadata.Name), s.Metadata.Name) {
		return errors.New("not confirmed")
	}

	ctx := context.Background()
	project := []string{"compose", "--project-name", s.Metadata.Name}
	if err := docker(ctx, nil, stdout, append(project, "stop")...); err != nil {
		return fmt.Errorf("stopping %s: %w", s.Metadata.Name, err)
	}
	var done, keep []string
	for _, v := range vols {
		safety := v + "-before-restore-" + stamp
		if err := docker(ctx, nil, stdout, "volume", "create", "--label", "nepfleet.restore.of="+v, safety); err != nil {
			return restoreFailed(stdout, s.Metadata.Name, done, keep, fmt.Errorf("safety copy of %s: %w", v, err))
		}
		if err := docker(ctx, nil, stdout, "run", "--rm", "--network", "none", "-v", v+":/from:ro", "-v", safety+":/to",
			snapshot.HelperImage, "cp", "-a", "/from/.", "/to/"); err != nil {
			return restoreFailed(stdout, s.Metadata.Name, done, keep, fmt.Errorf("safety copy of %s: %w", v, err))
		}
		keep = append(keep, safety)
		rc, err := open()
		if err != nil {
			return restoreFailed(stdout, s.Metadata.Name, done, keep, err)
		}
		// The volume name comes in as an environment variable, never as
		// shell text.
		err = docker(ctx, rc, stdout, "run", "--rm", "-i", "--network", "none", "-e", "VOL="+v, "-v", v+":/target",
			snapshot.HelperImage, "sh", "-c",
			`set -e; find /target -mindepth 1 -delete; tar -xzf - -C /target --strip-components=4 --numeric-owner --same-permissions "/backup/volumes/$VOL/_data"`)
		_ = rc.Close()
		if err != nil {
			return restoreFailed(stdout, s.Metadata.Name, done, keep, fmt.Errorf("restoring %s: %w", v, err))
		}
		done = append(done, v)
		fmt.Fprintf(stdout, "%s: restored\n", v)
	}
	if err := docker(ctx, nil, stdout, append(project, "start")...); err != nil {
		return fmt.Errorf("starting %s: %w", s.Metadata.Name, err)
	}
	fmt.Fprintf(stdout, "%s: restored and started. Check it, then remove the safety copies: docker volume rm %s\n", s.Metadata.Name, strings.Join(keep, " "))
	return nil
}

// projectVolumes maps the Docker volumes compose created for a project to
// their keys in the compose file.
func projectVolumes(project string) (map[string]string, error) {
	out, err := exec.Command("docker", "volume", "ls", "--filter", "label=com.docker.compose.project="+project,
		"--format", `{{.Name}} {{.Label "com.docker.compose.volume"}}`).Output()
	if err != nil {
		return nil, fmt.Errorf("listing volumes: %w", err)
	}
	vols := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name, key, ok := strings.Cut(line, " "); ok {
			vols[name] = key
		}
	}
	return vols, nil
}

func restoreFailed(stdout io.Writer, stack string, done, keep []string, err error) error {
	fmt.Fprintf(stdout, "%s: ✗ restore stopped; the stack stays stopped.\n", stack)
	if len(done) > 0 {
		fmt.Fprintf(stdout, "  already restored: %s\n", strings.Join(done, ", "))
	}
	if len(keep) > 0 {
		fmt.Fprintf(stdout, "  the data from before is in: %s\n", strings.Join(keep, ", "))
	}
	return err
}

// decrypt opens an archive, through age when it is encrypted.
func decrypt(path, identity string) (io.ReadCloser, error) {
	if !strings.HasSuffix(path, ".age") {
		return os.Open(path) //nolint:gosec // the archive the operator named
	}
	cmd := exec.Command("age", "--decrypt", "--identity", identity, "--", path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("age: %w", err)
	}
	return &cmdReader{ReadCloser: out, cmd: cmd}, nil
}

type cmdReader struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (r *cmdReader) Close() error {
	_ = r.ReadCloser.Close()
	return r.cmd.Wait()
}

// archiveVolumes lists the volumes in a docker-volume-backup archive
// (members /backup/volumes/<name>/_data/…), sorted.
func archiveVolumes(r io.Reader) ([]string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	set := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		rest, ok := strings.CutPrefix(strings.TrimPrefix(h.Name, "/"), "backup/volumes/")
		if !ok {
			continue
		}
		if name, after, ok := strings.Cut(rest, "/"); ok && (after == "_data" || strings.HasPrefix(after, "_data/")) {
			set[name] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	slices.Sort(out)
	return out, nil
}

func docker(ctx context.Context, stdin io.Reader, out io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // fixed commands; names are validated, never run through a shell
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, out, out
	return cmd.Run()
}
