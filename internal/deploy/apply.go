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

	"github.com/Serraniel/neptide/pkg/registry"

	"github.com/Serraniel/nepfleet/internal/lock"
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
	// Lock pins images to digests; nil disables pinning. Sync adds missing
	// images (resolved with Resolver) and drops unused ones; UpdateLock
	// re-resolves every image.
	Lock       *lock.Lock
	Resolver   lock.Resolver
	UpdateLock bool
}

// Result reports side effects the caller has to persist.
type Result struct {
	// LockChanged means Options.Lock differs and should be saved and committed.
	LockChanged bool
	// Unpinned lists images that could not be resolved and run by tag.
	Unpinned []string
}

// Apply writes the rendered files and brings the stack up. If the stack
// does not become healthy, the previous files are restored and re-applied.
func Apply(ctx context.Context, r Runner, p *Plan, out *render.Output, o Options) (Result, error) {
	var res Result
	if o.WaitTimeout <= 0 {
		o.WaitTimeout = 3 * time.Minute
	}
	if o.IngressNetwork != "" {
		if err := ensureNetwork(ctx, r, o.IngressNetwork, p.Mode == "swarm"); err != nil {
			return res, err
		}
	}
	prev, err := readManifest(p.Dir)
	if err != nil {
		return res, err
	}
	if err := backup(p.Dir, prev); err != nil {
		return res, fmt.Errorf("back up previous version: %w", err)
	}
	if err := writeFiles(p.Dir, out.Files, prev); err != nil {
		return res, err
	}
	var pins map[string]string
	if o.Lock != nil && o.Resolver != nil {
		imgs, err := images(ctx, r, p.Stack, p.Dir)
		if err == nil {
			resolver := o.Resolver
			if !o.UpdateLock {
				// New entries lock what already runs here, so adopting or
				// first-locking a stack never changes its images.
				resolver = localFirst{r: r, remote: o.Resolver}
			}
			res.LockChanged, res.Unpinned, err = o.Lock.Sync(ctx, resolver, imgs, o.UpdateLock)
		}
		if err != nil {
			if prev != nil {
				_ = restore(p.Dir, out.Files, prev)
			}
			return res, fmt.Errorf("pin images: %w", err)
		}
		pins = o.Lock.Images
	}
	err = up(ctx, r, p.Stack, p.Mode, p.Dir, pins, o.WaitTimeout)
	if err == nil {
		return res, writeManifest(p.Dir, out.Files, pins, o)
	}
	if prev == nil {
		// Nothing to go back to. Leave it as is for inspection.
		return res, fmt.Errorf("first deploy of %s failed (left running for inspection, `docker compose -p %s logs`): %w", p.Stack, p.Stack, err)
	}
	if rerr := restore(p.Dir, out.Files, prev); rerr != nil {
		return res, fmt.Errorf("%w; restoring the previous files also failed: %w", err, rerr)
	}
	// Re-pin the previous digests: the failed attempt may have moved local tags.
	if rerr := up(ctx, r, p.Stack, p.Mode, p.Dir, prev.Images, o.WaitTimeout); rerr != nil {
		return res, fmt.Errorf("%w; the previous version did not come back up either: %w", err, rerr)
	}
	res.LockChanged = false // the new pins never ran; do not ask to commit them
	return res, fmt.Errorf("%w: %w", ErrRolledBack, err)
}

// localFirst resolves from the local image's RepoDigests, then the registry.
type localFirst struct {
	r      Runner
	remote lock.Resolver
}

func (l localFirst) Digest(ctx context.Context, img string) (string, error) {
	if ref, err := registry.ParseRef(img); err == nil {
		if out, err := l.r.Run(ctx, "", "image", "inspect", "--format", "{{json .RepoDigests}}", img); err == nil {
			var digests []string
			if json.Unmarshal(out, &digests) == nil {
				for _, rd := range digests {
					if r, err := registry.ParseRef(rd); err == nil && r.Host == ref.Host && r.Repo == ref.Repo {
						return r.Digest, nil
					}
				}
			}
		}
	}
	return l.remote.Digest(ctx, img)
}

// images lists the interpolated image references of a stack.
func images(ctx context.Context, r Runner, stack, dir string) ([]string, error) {
	out, err := r.Run(ctx, dir, compose(stack, dir, "config", "--images")...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// pinLocal makes the local tag img point at digest, pulling it if needed.
// Compose then runs exactly that image while its config (and hash) still
// says img, so pinning never recreates containers by itself.
func pinLocal(ctx context.Context, r Runner, img, digest string) error {
	ref, err := registry.ParseRef(img)
	if err != nil {
		return err
	}
	byDigest := ref.Name() + "@" + digest
	want, err := r.Run(ctx, "", "image", "inspect", "--format", "{{.Id}}", byDigest)
	if err != nil {
		if _, err := r.Run(ctx, "", "pull", "--quiet", byDigest); err != nil {
			return err
		}
		if want, err = r.Run(ctx, "", "image", "inspect", "--format", "{{.Id}}", byDigest); err != nil {
			return err
		}
	}
	if have, err := r.Run(ctx, "", "image", "inspect", "--format", "{{.Id}}", img); err == nil && bytes.Equal(have, want) {
		return nil
	}
	_, err = r.Run(ctx, "", "tag", byDigest, img)
	return err
}

func compose(stack, dir string, args ...string) []string {
	return append([]string{"compose", "--project-name", stack, "--project-directory", dir,
		"--file", filepath.Join(dir, render.ComposeFile), "--env-file", filepath.Join(dir, render.EnvFile)}, args...)
}

func up(ctx context.Context, r Runner, stack, mode, dir string, pins map[string]string, wait time.Duration) error {
	if mode != "swarm" {
		for img, d := range pins {
			if err := pinLocal(ctx, r, img, d); err != nil {
				return fmt.Errorf("pin %s: %w", img, err)
			}
		}
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
	if swarmCfg, err = render.PinImages(swarmCfg, pins); err != nil {
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

func writeManifest(dir string, files map[string][]byte, pins map[string]string, o Options) error {
	m := Manifest{Commit: o.Commit, Dirty: o.Dirty, Images: pins}
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

// Adopt records out as already applied without touching any container: an
// imported stack's running containers are the applied state, so the first
// plan after an import is empty. Refuses if the stack was applied before.
func Adopt(dir string, out *render.Output) error {
	if m, err := readManifest(dir); err != nil {
		return err
	} else if m != nil {
		return fmt.Errorf("%s is already managed", dir)
	}
	if err := os.MkdirAll(filepath.Join(dir, metaDir), 0o700); err != nil {
		return err
	}
	if err := writeFiles(dir, out.Files, nil); err != nil {
		return err
	}
	return writeManifest(dir, out.Files, Options{})
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
