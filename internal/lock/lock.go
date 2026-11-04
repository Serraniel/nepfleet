// SPDX-License-Identifier: AGPL-3.0-or-later

// Package lock pins the images of a stack to digests (stacks/<name>/lock.yaml).
//
// A rollback to an older commit then restores exactly the images that ran
// at that commit, not whatever their tags point to today.
package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/neptide/pkg/registry"
)

// File is the lock file name inside a stack directory.
const File = "lock.yaml"

// Lock maps image references as written in the compose file (after
// interpolation, e.g. nginx:1.29-alpine) to the digest they resolved to.
type Lock struct {
	Images map[string]string `yaml:"images"`
}

// Load reads dir/lock.yaml; a missing file is an empty lock.
func Load(dir string) (*Lock, error) {
	l := &Lock{Images: map[string]string{}}
	raw, err := os.ReadFile(filepath.Join(dir, File))
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	} else if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(raw, l); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, File), err)
	}
	if l.Images == nil {
		l.Images = map[string]string{}
	}
	for ref, d := range l.Images {
		if !strings.HasPrefix(d, "sha256:") || len(d) != 71 {
			return nil, fmt.Errorf("%s: %s: %q is not a sha256 digest", File, ref, d)
		}
	}
	return l, nil
}

// Save writes the lock with a header, keys sorted (yaml sorts map keys).
func (l *Lock) Save(dir string) error {
	raw, err := yaml.Marshal(l)
	if err != nil {
		return err
	}
	header := "# Image digests, written by NepFleet on apply. Commit this file: it makes\n# every revision of the stack reproducible. Update with `nepfleet lock --update`.\n"
	return os.WriteFile(filepath.Join(dir, File), append([]byte(header), raw...), 0o644) //nolint:gosec // not secret, committed to git
}

// Resolver resolves a tag to its registry digest.
type Resolver interface {
	Digest(ctx context.Context, ref string) (string, error)
}

// Registry resolves through neptide's registry client.
type Registry struct{ Client *registry.Client }

// Digest implements Resolver.
func (r Registry) Digest(ctx context.Context, ref string) (string, error) {
	parsed, err := registry.ParseRef(ref)
	if err != nil {
		return "", err
	}
	return r.Client.Digest(ctx, parsed)
}

// Sync makes the lock cover exactly images: missing ones are resolved (all
// of them with update), unused ones dropped. Images that cannot be resolved
// (built locally, private without credentials) are returned as skipped and
// stay unpinned. changed reports whether the lock differs afterwards.
func (l *Lock) Sync(ctx context.Context, r Resolver, images []string, update bool) (changed bool, skipped []string, err error) {
	want := map[string]bool{}
	for _, img := range images {
		if strings.Contains(img, "@") {
			continue // already pinned in the compose file
		}
		want[img] = true
		if _, ok := l.Images[img]; ok && !update {
			continue
		}
		d, rerr := r.Digest(ctx, img)
		if rerr != nil {
			if ctx.Err() != nil {
				return changed, skipped, ctx.Err()
			}
			skipped = append(skipped, fmt.Sprintf("%s (%v)", img, rerr))
			continue
		}
		if l.Images[img] != d {
			l.Images[img] = d
			changed = true
		}
	}
	for img := range l.Images {
		if !want[img] {
			delete(l.Images, img)
			changed = true
		}
	}
	sort.Strings(skipped)
	return changed, skipped, nil
}
