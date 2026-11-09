// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"crypto/sha1" //nolint:gosec // git's blob hash, not security
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

// maxEditable is the largest file the editor shows or takes.
const maxEditable = 256 << 10

// gitBlob is a file's git blob hash, which a client sends back as its base.
func gitBlob(content []byte) string {
	h := sha1.New() //nolint:gosec // git's object id
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}

// editablePath says whether the editor may touch a path in a stack's directory:
// stack.yaml, a plain stack's compose.yaml, and what's under files/. Never
// encrypted values, lock.yaml or anything git keeps.
func editablePath(p string) bool {
	if p == "" || filepath.IsAbs(p) || strings.Contains(p, "\\") || p != filepath.ToSlash(filepath.Clean(p)) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		lower := strings.ToLower(part)
		// Case-insensitive file systems, and names Windows trims, as git checks too.
		if part == ".." || part == "." || lower == ".git" || lower == "git~1" ||
			strings.HasPrefix(lower, ".nepfleet") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
	}
	switch {
	case p == "stack.yaml", p == "compose.yaml":
		return true
	case strings.HasPrefix(p, "files/") && len(p) > len("files/"):
		return true
	}
	return false
}

// noSymlinks refuses a path if any part of it, below dir, is a symlink: even one
// pointing inside the stack (files/up -> ..) would reach lock.yaml or secrets.
// Writes then go through an os.Root, which never leaves dir either.
func noSymlinks(dir, rel string) error {
	cur := dir
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return api.ErrBadPath
		}
	}
	return nil
}

// refsOutside lists stacks that s newly references (compared with before) but
// that a token limited to allowed may not reach. Unchanged references stay.
func refsOutside(before, after *fleet.Stack, allowed []string) []string {
	if len(allowed) == 0 {
		return nil
	}
	var out []string
	for k, v := range after.Spec.Values {
		if !v.IsRef() || slices.Contains(allowed, v.From) {
			continue
		}
		if old, ok := before.Spec.Values[k]; ok && old == v {
			continue
		}
		if !slices.Contains(out, v.From) {
			out = append(out, v.From)
		}
	}
	sort.Strings(out)
	return out
}

// cleanReason is an error safe to show a client: no server paths, one line.
func cleanReason(err error, dirs ...string) string {
	msg := err.Error()
	for _, d := range dirs {
		if d != "" {
			msg = strings.ReplaceAll(msg, d+string(filepath.Separator), "")
			msg = strings.ReplaceAll(msg, d, ".")
		}
	}
	msg, _, _ = strings.Cut(msg, "\n")
	if len(msg) > 500 {
		msg = msg[:500] + "…"
	}
	return msg
}

// stackFiles lists a stack's editable text files.
func stackFiles(s *fleet.Stack) ([]api.StackFile, error) {
	var out []api.StackFile
	add := func(full, rel string) error {
		fi, err := os.Lstat(full)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxEditable || !editablePath(rel) {
			return nil //nolint:nilerr // not one the editor shows
		}
		raw, err := os.ReadFile(full) //nolint:gosec // a stack's own file
		if err != nil {
			return err
		}
		if !utf8.Valid(raw) {
			return nil
		}
		out = append(out, api.StackFile{Path: rel, Content: string(raw), Revision: gitBlob(raw)})
		return nil
	}
	for _, name := range []string{"stack.yaml", "compose.yaml"} {
		if err := add(filepath.Join(s.Dir, name), name); err != nil {
			return nil, err
		}
	}
	filesDir := filepath.Join(s.Dir, "files")
	err := filepath.WalkDir(filesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type()&os.ModeSymlink != 0 || d.IsDir() {
			return nil
		}
		return add(p, filepath.ToSlash(relTo(s.Dir, p)))
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// editStack writes a change to a stack's files, checks that the stack still loads
// and renders, and commits it as the person or token. On any refusal the files
// are as they were. It doesn't apply.
func editStack(c common, stack string, e api.StackEdit, by string, allowed []string) (api.EditResult, error) {
	if !apiMu.TryLock() {
		return api.EditResult{}, api.ErrBusy
	}
	defer apiMu.Unlock()
	out, err := withStack(c, stack, func(env *env, s *fleet.Stack) (any, error) {
		rel := relTo(env.fleet.Dir, s.Dir)
		if st, _ := gitOut(env.fleet.Dir, "status", "--porcelain", "--", rel); strings.TrimSpace(st) != "" {
			return nil, fmt.Errorf("%w (%s has uncommitted changes on the server)", api.ErrStale, rel)
		}
		root, err := os.OpenRoot(s.Dir)
		if err != nil {
			return nil, err
		}
		defer func() { _ = root.Close() }()
		type change struct {
			rel     string
			old     []byte
			existed bool
			newDir  string // the first directory this change creates, if any
		}
		var changes []change
		for p, content := range e.Files {
			if !editablePath(p) || len(content) > maxEditable || !utf8.ValidString(content) {
				return nil, api.ErrBadPath
			}
			if err := noSymlinks(s.Dir, p); err != nil {
				return nil, err
			}
			old, err := root.ReadFile(p)
			existed := err == nil
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			want := e.Base[p]
			if (existed && want != gitBlob(old)) || (!existed && want != "") {
				return nil, api.ErrStale
			}
			ch := change{rel: p, old: old, existed: existed}
			for d, prefix := strings.Split(p, "/"), ""; len(d) > 1; d = d[1:] {
				prefix = path.Join(prefix, d[0])
				if _, err := root.Lstat(prefix); errors.Is(err, os.ErrNotExist) {
					ch.newDir = prefix
					break
				}
			}
			changes = append(changes, ch)
		}
		// restore puts every file back; a failure is reported, not hidden, as
		// the stack's directory then differs from git.
		restore := func() error {
			var errs []error
			for _, ch := range changes {
				switch {
				case ch.existed:
					errs = append(errs, root.WriteFile(ch.rel, ch.old, 0o600))
				case ch.newDir != "":
					errs = append(errs, root.RemoveAll(ch.newDir))
				default:
					if err := root.Remove(ch.rel); err != nil && !errors.Is(err, os.ErrNotExist) {
						errs = append(errs, err)
					}
				}
			}
			if err := errors.Join(errs...); err != nil {
				log.Printf("api: restoring %s after a refused edit: %v", stack, err)
				return fmt.Errorf("restoring the files failed, %s differs from git: %w", rel, err)
			}
			return nil
		}
		fail := func(err error) (any, error) {
			if rerr := restore(); rerr != nil {
				return nil, errors.Join(err, rerr)
			}
			return nil, err
		}
		for _, ch := range changes {
			if err := root.MkdirAll(path.Dir(ch.rel), 0o750); err != nil {
				return fail(err)
			}
			if err := root.WriteFile(ch.rel, []byte(e.Files[ch.rel]), 0o600); err != nil {
				return fail(err)
			}
		}
		// The stack must still load (schema) and render, as apply needs.
		e2, err := c.open()
		var s2 *fleet.Stack
		if err == nil {
			if s2, err = e2.fleet.LoadStack(stack); err == nil {
				// A token limited to some stacks may not read others' values or
				// secrets by referencing them.
				if out := refsOutside(s, s2, allowed); len(out) > 0 {
					return fail(api.InvalidError{Reason: fmt.Sprintf("this token may not reference %s", strings.Join(out, ", "))})
				}
				_, err = e2.prepare(s2)
			}
		}
		if err != nil {
			// Not the error itself: a render error may quote a value, even a secret.
			log.Printf("api: an edit of %s was refused: the stack would no longer load or render", stack)
			return fail(api.InvalidError{Reason: cleanReason(err, s.Dir, env.fleet.Dir)})
		}
		paths := make([]string, 0, len(changes))
		for _, ch := range changes {
			paths = append(paths, filepath.Join(rel, filepath.FromSlash(ch.rel)))
		}
		sort.Strings(paths)
		if _, err := gitOut(env.fleet.Dir, append([]string{"add", "--"}, paths...)...); err != nil {
			_, _ = gitOut(env.fleet.Dir, append([]string{"reset", "-q", "--"}, paths...)...)
			return fail(err)
		}
		msg := strings.TrimSpace(strings.SplitN(e.Message, "\n", 2)[0])
		if msg == "" {
			msg = "chore(" + stack + "): edit over the API"
		}
		if err := commitAs(env.fleet.Dir, apiAuthor(by), msg, paths...); err != nil {
			_, _ = gitOut(env.fleet.Dir, append([]string{"reset", "-q", "--"}, paths...)...)
			return fail(err)
		}
		head, err := gitOut(env.fleet.Dir, "rev-parse", "HEAD")
		if err != nil {
			return nil, err
		}
		return api.EditResult{Commit: strings.TrimSpace(head)}, nil
	})
	if err != nil {
		return api.EditResult{}, err
	}
	return out.(api.EditResult), nil
}
