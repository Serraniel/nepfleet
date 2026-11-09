// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

func TestEditablePath(t *testing.T) {
	for p, want := range map[string]bool{
		"stack.yaml":            true,
		"compose.yaml":          true,
		"files/config/app.yaml": true,
		"files/":                false,
		"values.secret.yaml":    false,
		"lock.yaml":             false,
		"../other/stack.yaml":   false,
		"files/../stack.yaml":   false,
		"/etc/passwd":           false,
		".git/config":           false,
		"files/.git/x":          false,
		".nepfleet/manifest":    false,
		"files\\x":              false,
		"files/.GIT/config":     false,
		"files/Git~1/x":         false,
		"files/a.":              false,
		"files/a ":              false,
		".NEPFLEET":             false,
		"":                      false,
	} {
		if got := editablePath(p); got != want {
			t.Errorf("%q: %v", p, got)
		}
	}
}

func TestEditStack(t *testing.T) {
	c, dir, _ := rollbackFleet(t)
	stackDir := filepath.Join(dir, "stacks", "web")
	s, err := fleet.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.LoadStack("web")
	if err != nil {
		t.Fatal(err)
	}
	files, err := stackFiles(st)
	if err != nil {
		t.Fatal(err)
	}
	var compose api.StackFile
	for _, f := range files {
		if f.Path == "compose.yaml" {
			compose = f
		}
		if f.Path == fleet.SecretsFile {
			t.Error("encrypted values must never be listed")
		}
	}
	if compose.Revision == "" || !strings.Contains(compose.Content, "nginx:1.29") {
		t.Fatalf("files: %+v", files)
	}
	if got := tgit(t, dir, "hash-object", "stacks/web/compose.yaml"); got != compose.Revision {
		t.Errorf("revision %s is not git's %s", compose.Revision, got)
	}

	// A change with the right base is committed, as the person.
	newCompose := "services:\n  web:\n    image: nginx:1.30-alpine\n"
	res, err := editStack(c, "web", api.StackEdit{
		Files: map[string]string{"compose.yaml": newCompose, "files/config/app.conf": "x = 1\n"},
		Base:  map[string]string{"compose.yaml": compose.Revision},
	}, "Ada", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := tgit(t, dir, "log", "-1", "--format=%H|%an|%s"); !strings.HasPrefix(got, res.Commit+"|Ada|chore(web): edit over the API") {
		t.Errorf("commit: %s", got)
	}
	if raw, _ := os.ReadFile(filepath.Join(stackDir, "files/config/app.conf")); string(raw) != "x = 1\n" {
		t.Errorf("new file: %q", raw)
	}

	// The old base is stale now.
	if _, err := editStack(c, "web", api.StackEdit{Files: map[string]string{"compose.yaml": "x"}, Base: map[string]string{"compose.yaml": compose.Revision}}, "Ada", nil); !errors.Is(err, api.ErrStale) {
		t.Errorf("stale base: %v", err)
	}
	// A new file must not exist already.
	if _, err := editStack(c, "web", api.StackEdit{Files: map[string]string{"files/config/app.conf": "y"}}, "Ada", nil); !errors.Is(err, api.ErrStale) {
		t.Errorf("an existing file without a base: %v", err)
	}
	for _, p := range []string{"../x", "lock.yaml", fleet.SecretsFile, ".git/config"} {
		if _, err := editStack(c, "web", api.StackEdit{Files: map[string]string{p: "x"}}, "Ada", nil); !errors.Is(err, api.ErrBadPath) {
			t.Errorf("%s: %v", p, err)
		}
	}

	// A stack that no longer loads isn't saved, and its files stay as they were.
	head := tgit(t, dir, "rev-parse", "HEAD")
	stackYAML, _ := os.ReadFile(filepath.Join(stackDir, "stack.yaml"))
	var invalid api.InvalidError
	_, err = editStack(c, "web", api.StackEdit{
		Files: map[string]string{"stack.yaml": "kind: Nonsense\n", "files/new.txt": "n\n"},
		Base:  map[string]string{"stack.yaml": gitBlob(stackYAML)},
	}, "Ada", nil)
	if !errors.As(err, &invalid) {
		t.Fatalf("an invalid stack: %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(stackDir, "stack.yaml")); string(raw) != string(stackYAML) {
		t.Error("stack.yaml wasn't restored")
	}
	if _, err := os.Stat(filepath.Join(stackDir, "files/new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a new file of a refused edit stayed")
	}
	if got := tgit(t, dir, "rev-parse", "HEAD"); got != head {
		t.Error("a refused edit made a commit")
	}
	if s := tgit(t, dir, "status", "--porcelain"); s != "" {
		t.Errorf("left dirty: %s", s)
	}

	// A symlink pointing out of the stack is never followed.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(stackDir, "files", "out")); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "add", "-A")
	tgit(t, dir, "commit", "-qm", "symlink", "--no-verify")
	if _, err := editStack(c, "web", api.StackEdit{Files: map[string]string{"files/out/evil": "x"}}, "Ada", nil); !errors.Is(err, api.ErrBadPath) {
		t.Errorf("through a symlink: %v", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Error("a file was written outside the stack")
	}

	// Uncommitted changes on the server: refuse rather than mix them in.
	if err := os.WriteFile(filepath.Join(stackDir, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := editStack(c, "web", api.StackEdit{Files: map[string]string{"files/a": "a"}}, "Ada", nil); !errors.Is(err, api.ErrStale) {
		t.Errorf("uncommitted changes: %v", err)
	}
}

// A token limited to stacks can't reach another stack's values or secrets by
// writing a reference to it.
func TestEditStackRefsOutsideToken(t *testing.T) {
	c, dir, _ := rollbackFleet(t)
	stackDir := filepath.Join(dir, "stacks", "web")
	raw, _ := os.ReadFile(filepath.Join(stackDir, "stack.yaml"))
	withRef := "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: web}\nspec:\n  target: local\n  values:\n    PW: {from: db, secret: password}\n"
	var invalid api.InvalidError
	_, err := editStack(c, "web", api.StackEdit{Files: map[string]string{"stack.yaml": withRef}, Base: map[string]string{"stack.yaml": gitBlob(raw)}}, "ci", []string{"web"})
	if !errors.As(err, &invalid) || !strings.Contains(invalid.Reason, "may not reference db") {
		t.Fatalf("a reference outside the token: %v", err)
	}
	if now, _ := os.ReadFile(filepath.Join(stackDir, "stack.yaml")); string(now) != string(raw) {
		t.Error("stack.yaml wasn't restored")
	}
}

// A symlink pointing inside the stack still never lets an edit reach lock.yaml
// or encrypted values, and server paths stay out of refusals.
func TestEditStackSymlinkInside(t *testing.T) {
	c, dir, _ := rollbackFleet(t)
	stackDir := filepath.Join(dir, "stacks", "web")
	if err := os.MkdirAll(filepath.Join(stackDir, "files"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(stackDir, "files", "up")); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "add", "-A")
	tgit(t, dir, "commit", "-qm", "up", "--no-verify")
	for _, p := range []string{"files/up/lock.yaml", "files/up/" + fleet.SecretsFile, "files/up/x"} {
		if _, err := editStack(c, "web", api.StackEdit{Files: map[string]string{p: "x"}}, "Ada", nil); !errors.Is(err, api.ErrBadPath) {
			t.Errorf("%s: %v", p, err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(stackDir, "stack.yaml"))
	var invalid api.InvalidError
	_, err := editStack(c, "web", api.StackEdit{Files: map[string]string{"stack.yaml": "kind: [\n"}, Base: map[string]string{"stack.yaml": gitBlob(raw)}}, "Ada", nil)
	if !errors.As(err, &invalid) || strings.Contains(invalid.Reason, dir) {
		t.Errorf("a refusal shows server paths: %v", err)
	}
}
