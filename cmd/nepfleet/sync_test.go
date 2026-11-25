// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A fleet repository tracking a bare origin, and a second clone that pushes.
func syncFleet(t *testing.T) (c common, dir, other string) {
	t.Helper()
	c, dir, _ = rollbackFleet(t)
	t.Cleanup(func() { removeTestProject(t, c.stateDir) })
	origin := filepath.Join(t.TempDir(), "origin.git")
	tgit(t, t.TempDir(), "init", "-q", "--bare", origin)
	branch := tgit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	tgit(t, dir, "remote", "add", "origin", origin)
	tgit(t, dir, "push", "-q", "-u", "origin", branch)
	other = filepath.Join(t.TempDir(), "other")
	tgit(t, t.TempDir(), "clone", "-q", "-b", branch, origin, other)
	for _, kv := range [][2]string{{"commit.gpgsign", "false"}, {"user.name", "Ada"}, {"user.email", "ada@example.test"}} {
		tgit(t, other, "config", kv[0], kv[1])
	}
	return c, dir, other
}

func push(t *testing.T, repo, path, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	tgit(t, repo, "add", "-A")
	tgit(t, repo, "commit", "-qm", "change "+path, "--no-verify")
	tgit(t, repo, "push", "-q")
}

func TestPullFleet(t *testing.T) {
	c, dir, other := syncFleet(t)
	ctx := context.Background()
	e, err := c.open()
	if err != nil {
		t.Fatal(err)
	}
	// Nothing new.
	if p, err := pullFleet(ctx, e); err != nil || len(p.stacks) != 0 || p.from != p.to {
		t.Fatalf("up to date: %+v %v", p, err)
	}
	push(t, other, "stacks/web/compose.yaml", "services:\n  web:\n    image: nginx:1.30-alpine\n")
	p, err := pullFleet(ctx, e)
	if err != nil || !slices.Equal(p.stacks, []string{"web"}) || p.from == p.to {
		t.Fatalf("a change to web: %+v %v", p, err)
	}
	if head := tgit(t, dir, "rev-parse", "HEAD"); head != p.to {
		t.Errorf("not fast-forwarded: %s vs %s", head, p.to)
	}
	// Uncommitted changes on the server stop it, and touch nothing.
	if err := os.WriteFile(filepath.Join(dir, "stacks", "web", "compose.yaml"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pullFleet(ctx, e); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("uncommitted: %v", err)
	}
	tgit(t, dir, "checkout", "--", ".")
	// A local commit origin doesn't have, plus a new one there: diverged.
	push(t, other, "stacks/web/compose.yaml", "services:\n  web:\n    image: nginx:1.31-alpine\n")
	if err := os.WriteFile(filepath.Join(dir, "local.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "add", "-A")
	tgit(t, dir, "commit", "-qm", "local", "--no-verify")
	if _, err := pullFleet(ctx, e); !errors.Is(err, errDiverged) {
		t.Errorf("diverged: %v", err)
	}
	tgit(t, dir, "branch", "--unset-upstream")
	if _, err := pullFleet(ctx, e); err == nil || !strings.Contains(err.Error(), "upstream") {
		t.Errorf("no upstream: %v", err)
	}
}

// A changed stack set to apply: manual waits for a person.
func TestSyncPending(t *testing.T) {
	c, _, other := syncFleet(t)
	s := newSyncer(c, 0, io.Discard)
	push(t, other, "stacks/web/compose.yaml", "services:\n  web:\n    image: nginx:1.30-alpine\n")
	s.once(context.Background())
	st := s.Status()
	if st.Error != "" || !slices.Equal(st.Pending, []string{"web"}) || len(st.Applied) != 0 || st.Commit == "" || st.Last.IsZero() {
		t.Errorf("status: %+v", st)
	}
	// Nothing new: nothing pending from this round.
	s.once(context.Background())
	if st := s.Status(); len(st.Pending) != 0 || st.Error != "" {
		t.Errorf("second round: %+v", st)
	}
}

func TestFleetRemote(t *testing.T) {
	dir := t.TempDir()
	tgit(t, dir, "init", "-q")
	if url, ok, err := fleetRemote(dir); err != nil || ok || url != "" {
		t.Errorf("no remote: %q %v %v", url, ok, err)
	}
	tgit(t, dir, "remote", "add", "backup", "https://user:tok3n@git.example.test/b.git")
	if url, ok, err := fleetRemote(dir); err != nil || !ok || url != "https://git.example.test/b.git" {
		t.Errorf("one remote: %q %v %v", url, ok, err)
	}
	// origin wins over the others, and the branch's upstream over origin.
	tgit(t, dir, "remote", "add", "origin", "git@git.example.test:fleet/infra.git")
	if url, _, _ := fleetRemote(dir); url != "git@git.example.test:fleet/infra.git" {
		t.Errorf("origin: %q", url)
	}
	branch := tgit(t, dir, "symbolic-ref", "--short", "HEAD")
	tgit(t, dir, "config", "branch."+branch+".remote", "backup")
	if url, _, _ := fleetRemote(dir); url != "https://git.example.test/b.git" {
		t.Errorf("upstream: %q", url)
	}
	if _, _, err := fleetRemote(filepath.Join(dir, "missing")); err == nil {
		t.Error("no repository: no error")
	}
}

func TestDisplayRemote(t *testing.T) {
	for raw, want := range map[string]string{
		"https://git.example.test/fleet.git":               "https://git.example.test/fleet.git",
		"https://oauth2:secret@git.example.test/fleet.git": "https://git.example.test/fleet.git",
		"https://tok@git.example.test/f.git?x=secret#frag": "https://git.example.test/f.git",
		"ssh://git@git.example.test:2222/fleet.git":        "ssh://git.example.test:2222/fleet.git",
		"git@git.example.test:fleet.git":                   "git@git.example.test:fleet.git",
		"git.example.test:fleet.git":                       "git.example.test:fleet.git",
		"user:secret@git.example.test:fleet.git":           "",
		"/srv/git/fleet.git":                               "/srv/git/fleet.git",
		"file:///srv/git/fleet.git":                        "file:///srv/git/fleet.git",
		"https://%zz@host/x":                               "",
		"something odd":                                    "",
	} {
		if got := displayRemote(raw); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}
