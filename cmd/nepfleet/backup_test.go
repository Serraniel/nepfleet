// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "user.name=t", "-c", "user.email=t@example.org", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitCommit(t *testing.T, dir, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(msg), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", msg)
	return git(t, dir, "rev-parse", "HEAD")
}

// fleetClone makes origin (bare), a fleet clone of it and a mirror (bare).
func fleetClone(t *testing.T) (root, clone, mirror string) {
	t.Helper()
	root = t.TempDir()
	origin, mirror := filepath.Join(root, "origin.git"), filepath.Join(root, "mirror.git")
	git(t, root, "init", "-q", "--bare", origin)
	git(t, root, "init", "-q", "--bare", mirror)
	clone = filepath.Join(root, "fleet")
	if err := run([]string{"init", clone}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(clone, "fleet.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), "spec:\n", "spec:\n  mirrors: [\""+mirror+"\"]\n", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "init", "-q")
	gitCommit(t, clone, "init")
	git(t, clone, "remote", "add", "origin", origin)
	git(t, clone, "push", "-q", "origin", "main")
	return root, clone, mirror
}

func mirrorCmd(clone, state string) error {
	return run([]string{"mirror", "--fleet", clone, "--state", state}, io.Discard, io.Discard)
}

func TestMirrorNeverDeletesOrRewinds(t *testing.T) {
	root, clone, mirror := fleetClone(t)
	state := t.TempDir()
	if err := mirrorCmd(clone, state); err != nil {
		t.Fatal(err)
	}
	// Someone else's branch on the mirror survives.
	other := filepath.Join(root, "other")
	git(t, root, "clone", "-q", mirror, other)
	git(t, other, "switch", "-q", "-c", "theirs")
	gitCommit(t, other, "theirs")
	git(t, other, "push", "-q", "origin", "theirs")
	// origin moves on through another clone; this clone stays behind.
	second := filepath.Join(root, "second")
	git(t, root, "clone", "-q", filepath.Join(root, "origin.git"), second)
	newest := gitCommit(t, second, "new")
	git(t, second, "push", "-q", "origin", "main")
	if err := mirrorCmd(clone, state); err != nil {
		t.Fatal(err)
	}
	if got := git(t, mirror, "rev-parse", "main"); got != newest {
		t.Errorf("mirror main = %s, want origin's %s (a stale clone must not rewind it)", got, newest)
	}
	if git(t, mirror, "branch", "--list", "theirs") == "" {
		t.Error("the mirror's own branch was deleted")
	}
	// A mirror that diverged is not overwritten: mirror fails.
	gitCommit(t, other, "diverge")
	git(t, other, "push", "-q", "origin", "theirs:main", "--force")
	diverged := git(t, mirror, "rev-parse", "main")
	gitCommit(t, second, "newer")
	git(t, second, "push", "-q", "origin", "main")
	if err := mirrorCmd(clone, state); err == nil {
		t.Error("a diverged mirror must make mirror fail")
	}
	if got := git(t, mirror, "rev-parse", "main"); got != diverged {
		t.Error("a diverged mirror was overwritten")
	}
}

func TestKeyBackupTarget(t *testing.T) {
	_, clone, _ := fleetClone(t)
	outside := t.TempDir()
	if err := os.Symlink(clone, filepath.Join(outside, "lnk")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(clone, "k.age"), filepath.Join(outside, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(clone, "stacks"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{
		filepath.Join(clone, "k.age"),
		filepath.Join(outside, "lnk", "k.age"),
		filepath.Join(outside, "dangling"),
	} {
		if _, err := backupTarget(clone, out); err == nil {
			t.Errorf("%s: accepted", out)
		}
	}
	// --fleet given as a subfolder or through a symlink: the git root counts.
	if _, err := backupTarget(filepath.Join(clone, "stacks"), filepath.Join(clone, "k.age")); err == nil {
		t.Error("a subfolder --fleet let the copy into the repository")
	}
	if _, err := backupTarget(filepath.Join(outside, "lnk"), filepath.Join(clone, "k.age")); err == nil {
		t.Error("a symlinked --fleet let the copy into the repository")
	}
	if _, err := backupTarget(clone, filepath.Join(outside, "copy.age")); err != nil {
		t.Errorf("outside the repository: %v", err)
	}
}

func TestKeyBackupFile(t *testing.T) {
	for _, tool := range []string{"age", "age-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	_, clone, _ := fleetClone(t)
	dir := t.TempDir()
	key, second := filepath.Join(dir, "key.txt"), filepath.Join(dir, "second.txt")
	for _, k := range []string{key, second} {
		if out, err := exec.Command("age-keygen", "-o", k).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	pub, err := exec.Command("age-keygen", "-y", second).Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", key)
	out := filepath.Join(dir, "copy.age")
	if err := run([]string{"key", "backup", "--fleet", clone, "--state", dir, "--out", out, "--recipient", strings.TrimSpace(string(pub))}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("copy is %v, want owner-only", fi.Mode().Perm())
	}
	got, err := exec.Command("age", "-d", "-i", second, "--", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(key)
	if string(got) != string(want) {
		t.Error("the copy does not decrypt to the key")
	}
	if rec := readKeyBackup(dir); rec == nil || rec.File != out || rec.Method != "recipients" {
		t.Errorf("record: %+v", rec)
	}
}
