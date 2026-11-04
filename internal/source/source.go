// SPDX-License-Identifier: AGPL-3.0-or-later

// Package source resolves template sources from fleet.yaml to directories:
// local paths as they are, git repositories cloned into a cache and checked
// out at their ref.
package source

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

// Resolver turns sources into directories.
type Resolver struct {
	Fleet *fleet.Fleet
	// Cache holds clones; defaults to $XDG_CACHE_HOME/nepfleet/sources.
	Cache string
	// Offline skips fetching and uses the cached clone as is.
	Offline bool

	resolved map[string]Resolved
}

// Resolved is a source at a concrete revision.
type Resolved struct {
	Dir    string
	Commit string // empty for local paths outside git
}

// Resolve returns the source's directory, fetching it once per Resolver.
func (r *Resolver) Resolve(name string) (Resolved, error) {
	if res, ok := r.resolved[name]; ok {
		return res, nil
	}
	src, ok := r.Fleet.Source(name)
	if !ok {
		return Resolved{}, fmt.Errorf("unknown template source %q (see fleet.yaml spec.templateSources)", name)
	}
	var res Resolved
	var err error
	if src.Path != "" {
		dir := src.Path
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(r.Fleet.Dir, dir)
		}
		res = Resolved{Dir: dir}
		res.Commit, _ = git(dir, "rev-parse", "HEAD")
	} else {
		res, err = r.clone(src)
	}
	if err != nil {
		return Resolved{}, err
	}
	if r.resolved == nil {
		r.resolved = map[string]Resolved{}
	}
	r.resolved[name] = res
	return res, nil
}

// Template loads a template by name from a source. Templates live in
// <source>/system/<name> or <source>/apps/<name>.
func (r *Resolver) Template(ref fleet.TemplateRef) (*template.Template, Resolved, error) {
	res, err := r.Resolve(ref.Source)
	if err != nil {
		return nil, res, err
	}
	for _, tier := range []string{"system", "apps"} {
		dir := filepath.Join(res.Dir, tier, ref.Name)
		if _, err := os.Stat(filepath.Join(dir, "template.yaml")); err != nil {
			continue
		}
		t, err := template.Load(dir)
		if err != nil {
			return nil, res, err
		}
		if err := t.Validate(); err != nil {
			return nil, res, fmt.Errorf("template %s/%s is invalid: %w", ref.Source, ref.Name, err)
		}
		if ref.Version != "" && t.Spec.Version != ref.Version {
			return nil, res, fmt.Errorf("template %s/%s is version %s, stack pins %s (upgrade with a migration, or change the pin)",
				ref.Source, ref.Name, t.Spec.Version, ref.Version)
		}
		return t, res, nil
	}
	return nil, res, fmt.Errorf("template %q not found in source %q", ref.Name, ref.Source)
}

func (r *Resolver) clone(src fleet.Source) (Resolved, error) {
	cache := r.Cache
	if cache == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return Resolved{}, err
		}
		cache = filepath.Join(base, "nepfleet", "sources")
	}
	dir := filepath.Join(cache, src.Name)
	ref := src.Ref
	if ref == "" {
		ref = "main"
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		if r.Offline {
			return Resolved{}, fmt.Errorf("source %s is not cached and --offline is set", src.Name)
		}
		if err := os.MkdirAll(cache, 0o750); err != nil {
			return Resolved{}, err
		}
		if _, err := git(cache, "clone", "--quiet", "--", src.URL, src.Name); err != nil {
			return Resolved{}, err
		}
	} else if url, _ := git(dir, "remote", "get-url", "origin"); url != src.URL {
		return Resolved{}, fmt.Errorf("cached source %s points at %s, fleet.yaml says %s; remove %s", src.Name, url, src.URL, dir)
	} else if !r.Offline {
		if _, err := git(dir, "fetch", "--quiet", "--tags", "--force", "origin"); err != nil {
			return Resolved{}, err
		}
	}
	// A branch follows origin; a tag or commit is used as is.
	target := ref
	if _, err := git(dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+ref); err == nil {
		target = "origin/" + ref
	}
	if _, err := git(dir, "checkout", "--quiet", "--detach", target); err != nil {
		return Resolved{}, fmt.Errorf("source %s: ref %q: %w", src.Name, ref, err)
	}
	commit, err := git(dir, "rev-parse", "HEAD")
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Dir: dir, Commit: commit}, nil
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
