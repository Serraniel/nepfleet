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
	// DataSafety, when set, snapshots the running stack's volumes before
	// the new version starts and restores them on rollback (compose only).
	DataSafety *DataSafety
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
	var imgs []string
	if o.Lock != nil && o.Resolver != nil {
		imgs, err = images(ctx, r, p.Stack, p.Dir)
		if err == nil {
			resolver := o.Resolver
			if !o.UpdateLock {
				// New entries lock what is running now, so adopting or first
				// locking a stack never changes its images.
				resolver = &runningFirst{r: r, stack: p.Stack, swarm: p.Mode == "swarm", remote: o.Resolver}
			}
			res.LockChanged, res.Unpinned, err = o.Lock.Sync(ctx, resolver, imgs, o.UpdateLock)
		}
		if err != nil {
			res.LockChanged = false
			if prev != nil {
				_ = restore(p.Dir, out.Files, prev)
			}
			return res, fmt.Errorf("pin images: %w", err)
		}
		pins = o.Lock.Images
	}
	// What runs now, so a rollback restores it even for images the previous
	// manifest did not pin: local tags for compose, service digests for swarm
	// (a plain-tag stack deploy would resolve the moved tag at the registry).
	tagsBefore := localTags(ctx, r, imgs)
	rollbackPins := map[string]string{}
	if prev != nil {
		for img, d := range prev.Images {
			rollbackPins[img] = d
		}
		if p.Mode == "swarm" {
			running := (&runningFirst{r: r, stack: p.Stack, swarm: true}).collect(ctx)
			for img, d := range running {
				if _, pinned := rollbackPins[img]; !pinned {
					rollbackPins[img] = d
				}
			}
		}
	}
	var g *guard
	if o.DataSafety != nil && prev != nil && p.Mode != "swarm" {
		if g, err = o.DataSafety.take(ctx, r, p.Stack, p.Dir); err != nil {
			res.LockChanged = false
			_ = restore(p.Dir, out.Files, prev)
			return res, err
		}
	}
	err = up(ctx, r, p.Stack, p.Mode, p.Dir, pins, o.WaitTimeout)
	if err == nil {
		return res, writeManifest(p.Dir, out.Files, pins, o)
	}
	res.LockChanged = false // the new pins did not run successfully; never ask to commit them
	if prev == nil {
		// Nothing to go back to. Leave it as is for inspection.
		return res, fmt.Errorf("first deploy of %s failed (left as is for inspection, `docker compose -p %s logs`): %w", p.Stack, p.Stack, err)
	}
	if rerr := restore(p.Dir, out.Files, prev); rerr != nil {
		return res, fmt.Errorf("%w; restoring the previous files also failed: %w", err, rerr)
	}
	// The new version may have migrated the data: put it back before the
	// previous version starts on it, or leave the stack stopped.
	if rerr := g.restore(ctx, r, p.Stack, p.Dir); rerr != nil {
		return res, fmt.Errorf("%w; restoring the data snapshot failed, so the stack stays stopped rather than run the previous version on changed data: %w", err, rerr)
	}
	// The failed attempt may have moved local tags: put back what they were,
	// then re-pin the previous digests on top.
	for img, id := range tagsBefore {
		if _, pinned := prev.Images[img]; !pinned {
			if _, terr := r.Run(ctx, "", "tag", id, img); terr != nil {
				return res, fmt.Errorf("%w; restoring the previous image of %s failed: %w", err, img, terr)
			}
		}
	}
	if rerr := up(ctx, r, p.Stack, p.Mode, p.Dir, rollbackPins, o.WaitTimeout); rerr != nil {
		return res, fmt.Errorf("%w; the previous version did not come back up either: %w", err, rerr)
	}
	return res, fmt.Errorf("%w: %w", ErrRolledBack, err)
}

// ResolveUpdate returns a copy of l with every image of the rendered stack
// re-resolved to its current registry digest, without touching the stack:
// the files are rendered into a private temporary directory to let compose
// interpolate the image references. Used to show updates before confirming.
func ResolveUpdate(ctx context.Context, r Runner, stack string, out *render.Output, l *lock.Lock, resolver lock.Resolver) (*lock.Lock, []string, error) {
	tmp, err := os.MkdirTemp("", "nepfleet-resolve-")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }() // MkdirTemp creates it 0700: the env file stays private
	if err := writeFiles(tmp, out.Files, nil); err != nil {
		return nil, nil, err
	}
	imgs, err := images(ctx, r, stack, tmp)
	if err != nil {
		return nil, nil, err
	}
	next := &lock.Lock{Images: map[string]string{}}
	for k, v := range l.Images {
		next.Images[k] = v
	}
	_, skipped, err := next.Sync(ctx, resolver, imgs, true)
	return next, skipped, err
}

// localTags maps each image reference to the image ID its local tag points at.
func localTags(ctx context.Context, r Runner, imgs []string) map[string]string {
	out := map[string]string{}
	for _, img := range imgs {
		if id, err := r.Run(ctx, "", "image", "inspect", "--format", "{{.Id}}", img); err == nil {
			if s := strings.TrimSpace(string(id)); s != "" {
				out[img] = s
			}
		}
	}
	return out
}

// runningFirst resolves an image to the digest that is running now: for
// swarm the digest in the service spec (the manager's local images may be
// stale), for compose the image of the stack's running containers, then the
// local tag, and only then the registry.
type runningFirst struct {
	r       Runner
	stack   string
	swarm   bool
	remote  lock.Resolver
	running map[string]string
}

func (l *runningFirst) Digest(ctx context.Context, img string) (string, error) {
	if l.running == nil {
		l.running = l.collect(ctx)
	}
	if d, ok := l.running[img]; ok {
		return d, nil
	}
	if !l.swarm {
		if d := repoDigest(ctx, l.r, img, img); d != "" {
			return d, nil
		}
	}
	return l.remote.Digest(ctx, img)
}

func (l *runningFirst) collect(ctx context.Context) map[string]string {
	out := map[string]string{}
	if l.swarm {
		names, err := l.r.Run(ctx, "", "service", "ls", "--filter", "label=com.docker.stack.namespace="+l.stack, "--format", "{{.Name}}")
		if err != nil {
			return out
		}
		for _, svc := range strings.Fields(string(names)) {
			spec, err := l.r.Run(ctx, "", "service", "inspect", "--format", "{{.Spec.TaskTemplate.ContainerSpec.Image}}", svc)
			if err != nil {
				continue
			}
			if name, d, ok := strings.Cut(strings.TrimSpace(string(spec)), "@"); ok {
				out[name] = d
			}
		}
		return out
	}
	ids, err := l.r.Run(ctx, "", "ps", "--all", "--quiet", "--filter", "label=com.docker.compose.project="+l.stack)
	if err != nil {
		return out
	}
	for _, id := range strings.Fields(string(ids)) {
		info, err := l.r.Run(ctx, "", "inspect", "--format", "{{.Config.Image}}|{{.Image}}", id)
		if err != nil {
			continue
		}
		img, imageID, _ := strings.Cut(strings.TrimSpace(string(info)), "|")
		if d := repoDigest(ctx, l.r, imageID, img); d != "" {
			out[img] = d
		}
	}
	return out
}

// repoDigest returns the RepoDigest of image (ID or reference) for ref's repository.
func repoDigest(ctx context.Context, r Runner, image, ref string) string {
	want, err := registry.ParseRef(ref)
	if err != nil {
		return ""
	}
	raw, err := r.Run(ctx, "", "image", "inspect", "--format", "{{json .RepoDigests}}", image)
	if err != nil {
		return ""
	}
	var digests []string
	if json.Unmarshal(raw, &digests) != nil {
		return ""
	}
	for _, rd := range digests {
		if got, err := registry.ParseRef(rd); err == nil && got.Host == want.Host && got.Repo == want.Repo {
			return got.Digest
		}
	}
	return ""
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
	return writeManifest(dir, out.Files, nil, Options{}) // pins come from the running containers on the first apply
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
