// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/render"
)

// ErrRolledBack means the new version failed and the previous one was
// re-applied. The error wraps the cause.
var ErrRolledBack = errors.New("rolled back to the previous version")

// Runner executes docker commands. Docker is the real implementation.
type Runner interface {
	Run(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// Docker runs the docker CLI.
type Docker struct{}

// Run executes docker with args in dir.
func (Docker) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("docker %s: %w: %s", strings.Join(args[:min(3, len(args))], " "), err, tail(stderr.String(), 4))
	}
	return out, nil
}

// Options tune Apply.
type Options struct {
	// WaitTimeout bounds the health gate.
	WaitTimeout time.Duration
	// Commit and Dirty describe the fleet revision being applied.
	Commit string
	Dirty  bool
	// IngressNetwork is created if missing.
	IngressNetwork string
}

// Apply writes the rendered files and brings the stack up. If the stack
// does not become healthy, the previous files are restored and re-applied.
func Apply(ctx context.Context, r Runner, p *Plan, out *render.Output, o Options) error {
	if o.WaitTimeout <= 0 {
		o.WaitTimeout = 3 * time.Minute
	}
	if o.IngressNetwork != "" {
		if err := ensureNetwork(ctx, r, o.IngressNetwork, p.Mode == "swarm"); err != nil {
			return err
		}
	}
	prev, err := readManifest(p.Dir)
	if err != nil {
		return err
	}
	if err := backup(p.Dir, prev); err != nil {
		return fmt.Errorf("back up previous version: %w", err)
	}
	if err := writeFiles(p.Dir, out.Files, prev); err != nil {
		return err
	}
	err = up(ctx, r, p.Stack, p.Mode, p.Dir, o.WaitTimeout)
	if err == nil {
		return writeManifest(p.Dir, out.Files, o)
	}
	if prev == nil {
		// Nothing to go back to. Leave it as is for inspection.
		return fmt.Errorf("first deploy of %s failed (left running for inspection, `docker compose -p %s logs`): %w", p.Stack, p.Stack, err)
	}
	if rerr := restore(p.Dir, out.Files, prev); rerr != nil {
		return fmt.Errorf("%w; restoring the previous files also failed: %w", err, rerr)
	}
	if rerr := up(ctx, r, p.Stack, p.Mode, p.Dir, o.WaitTimeout); rerr != nil {
		return fmt.Errorf("%w; the previous version did not come back up either: %w", err, rerr)
	}
	return fmt.Errorf("%w: %w", ErrRolledBack, err)
}

func compose(stack, dir string, args ...string) []string {
	return append([]string{"compose", "--project-name", stack, "--project-directory", dir,
		"--file", filepath.Join(dir, render.ComposeFile), "--env-file", filepath.Join(dir, render.EnvFile)}, args...)
}

func up(ctx context.Context, r Runner, stack, mode, dir string, wait time.Duration) error {
	if mode != "swarm" {
		_, err := r.Run(ctx, dir, compose(stack, dir, "up", "--detach", "--remove-orphans", "--wait",
			"--wait-timeout", fmt.Sprint(int(wait.Seconds())))...)
		return err
	}
	// docker stack deploy does not read .env: interpolate with compose first.
	cfg, err := r.Run(ctx, dir, compose(stack, dir, "config", "--format", "yaml")...)
	if err != nil {
		return err
	}
	swarmCfg, err := render.ForSwarm(cfg)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, metaDir, "stack.yaml")
	if err := os.WriteFile(path, swarmCfg, 0o600); err != nil {
		return err
	}
	_, err = r.Run(ctx, dir, "stack", "deploy", "--detach=false", "--prune", "--with-registry-auth", "--compose-file", path, stack)
	return err
}

func ensureNetwork(ctx context.Context, r Runner, name string, swarm bool) error {
	if _, err := r.Run(ctx, "", "network", "inspect", name); err == nil {
		return nil
	}
	args := []string{"network", "create"}
	if swarm {
		args = append(args, "--driver", "overlay", "--attachable")
	}
	_, err := r.Run(ctx, "", append(args, name)...)
	return err
}

// fileMode keeps the env file (it holds secrets) private.
func fileMode(path string) os.FileMode {
	if path == render.EnvFile {
		return 0o600
	}
	return 0o644
}

func writeFiles(dir string, files map[string][]byte, prev *Manifest) error {
	for path, content := range files {
		if !filepath.IsLocal(path) {
			return fmt.Errorf("refusing to write outside the stack directory: %s", path)
		}
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil { //nolint:gosec // bind-mounted into containers
			return err
		}
		if err := os.WriteFile(full, content, fileMode(path)); err != nil {
			return err
		}
	}
	if prev != nil {
		for _, path := range prev.Files {
			if _, ok := files[path]; !ok && filepath.IsLocal(path) {
				if err := os.Remove(filepath.Join(dir, path)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
		}
	}
	return nil
}

// backup copies the managed files of the current version to .nepfleet/prev.
func backup(dir string, man *Manifest) error {
	prevDir := filepath.Join(dir, metaDir, "prev")
	if err := os.RemoveAll(prevDir); err != nil {
		return err
	}
	if man == nil {
		return os.MkdirAll(filepath.Join(dir, metaDir), 0o700)
	}
	for _, path := range man.Files {
		raw, err := os.ReadFile(filepath.Join(dir, path))
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		dst := filepath.Join(prevDir, path)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, raw, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// restore puts the backed-up version back and removes files it did not have.
func restore(dir string, current map[string][]byte, prev *Manifest) error {
	prevDir := filepath.Join(dir, metaDir, "prev")
	files := map[string][]byte{}
	for _, path := range prev.Files {
		raw, err := os.ReadFile(filepath.Join(prevDir, path))
		if err != nil {
			return err
		}
		files[path] = raw
	}
	var currentManifest = &Manifest{}
	for path := range current {
		currentManifest.Files = append(currentManifest.Files, path)
	}
	return writeFiles(dir, files, currentManifest)
}

func writeManifest(dir string, files map[string][]byte, o Options) error {
	m := Manifest{Commit: o.Commit, Dirty: o.Dirty}
	for path := range files {
		m.Files = append(m.Files, path)
	}
	sort.Strings(m.Files)
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, metaDir, "manifest.json"), raw, 0o600)
}

// Applied returns the manifest of the last successful apply, or nil.
func Applied(dir string) (*Manifest, error) { return readManifest(dir) }

// tail keeps the last n non-empty lines: compose prints progress first and
// the reason for a failure last.
func tail(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines[max(0, len(lines)-n):], " | ")
}
