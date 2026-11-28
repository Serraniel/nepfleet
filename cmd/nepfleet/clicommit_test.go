// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runNew(t *testing.T, c common, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{"hello", "greeter", "--source", "local", "--set", "GREETING=hi", "--fleet", c.fleetDir, "--state", c.stateDir, "--host", c.host, "--offline"}, extra...)
	var out, errs bytes.Buffer
	err := cmdNew(args, &out, &errs)
	return out.String(), err
}

// A change of someone else's, staged: a commit of the CLI's must not take it along.
func stageOther(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "stacks", "web", "compose.yaml"), []byte("services:\n  web:\n    image: nginx:1.30-alpine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "add", "stacks/web/compose.yaml")
}

func TestCmdNewCommits(t *testing.T) {
	c, dir := newStackFleet(t)
	stageOther(t, dir)
	out, err := runNew(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "committed stacks/greeter\n") || strings.Contains(out, "then commit") {
		t.Errorf("output:\n%s", out)
	}
	if got := tgit(t, dir, "log", "-1", "--format=%an|%s"); got != "Server|feat(greeter): new stack from hello" {
		t.Errorf("commit: %s", got)
	}
	if got := tgit(t, dir, "show", "--name-only", "--format=", "HEAD"); got != "stacks/greeter/stack.yaml" {
		t.Errorf("committed files: %q", got)
	}
	if got := tgit(t, dir, "status", "--porcelain"); got != "M  stacks/web/compose.yaml" {
		t.Errorf("the other change: %q", got)
	}
}

func TestCmdNewNoCommit(t *testing.T) {
	c, dir := newStackFleet(t)
	head := tgit(t, dir, "rev-parse", "HEAD")
	out, err := runNew(t, c, "--no-commit")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "then commit stacks/greeter") {
		t.Errorf("output:\n%s", out)
	}
	if got := tgit(t, dir, "rev-parse", "HEAD"); got != head {
		t.Error("committed despite --no-commit")
	}
	if got := tgit(t, dir, "status", "--porcelain"); got != "?? stacks/greeter/" {
		t.Errorf("status: %q", got)
	}
}

func TestCmdNewNotARepository(t *testing.T) {
	c, dir := newStackFleet(t)
	if err := os.Rename(filepath.Join(dir, ".git"), filepath.Join(dir, "git-away")); err != nil {
		t.Fatal(err)
	}
	out, err := runNew(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "then commit stacks/greeter") {
		t.Errorf("output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "stacks", "greeter", "stack.yaml")); err != nil {
		t.Error(err)
	}
}

// Changes git still holds for the directory (here a staged file deleted since)
// stay an error, before anything is asked or written.
func TestCmdNewRefusesDirtyDirectory(t *testing.T) {
	c, dir := newStackFleet(t)
	st := filepath.Join(dir, "stacks", "greeter")
	if err := os.MkdirAll(st, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st, "old.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "add", "stacks/greeter/old.txt")
	if err := os.Remove(filepath.Join(st, "old.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(st); err != nil {
		t.Fatal(err)
	}
	if _, err := runNew(t, c); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(st); !errors.Is(err, os.ErrNotExist) {
		t.Error("files were written")
	}
}

// Without a git identity (inside the container, say), the commit names the
// server's identity instead of failing.
func TestCmdNewWithoutIdentity(t *testing.T) {
	c, dir := newStackFleet(t)
	tgit(t, dir, "config", "--unset", "user.name")
	tgit(t, dir, "config", "--unset", "user.email")
	tgit(t, dir, "config", "user.useConfigOnly", "true")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	if w := cliCommitter(dir); w.author == "" {
		t.Fatal("found an identity where there is none")
	}
	if _, err := runNew(t, c); err != nil {
		t.Fatal(err)
	}
	want := "nepfleet <nepfleet@tokens.nepfleet.invalid>|NepFleet <nepfleet@localhost>"
	if got := tgit(t, dir, "log", "-1", "--format=%an <%ae>|%cn <%ce>"); got != want {
		t.Errorf("identity: %s, want %s", got, want)
	}
}

// What an apply wrote is committed as whoever ran it, and nothing else.
func TestRecordApplyCLI(t *testing.T) {
	c, dir, _ := rollbackFleet(t)
	stageOther(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "stacks", "web", "lock.yaml"), []byte("images: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	recordApply(c, "web", cliCommitter, &log)
	if got := log.String(); got != "committed stacks/web/lock.yaml\n" {
		t.Errorf("log: %q", got)
	}
	if got := tgit(t, dir, "log", "-1", "--format=%an|%s"); got != "Server|chore(web): record the apply" {
		t.Errorf("commit: %s", got)
	}
	if got := tgit(t, dir, "show", "--name-only", "--format=", "HEAD"); got != "stacks/web/lock.yaml" {
		t.Errorf("committed files: %q", got)
	}
	if got := tgit(t, dir, "status", "--porcelain"); got != "M  stacks/web/compose.yaml" {
		t.Errorf("the other change: %q", got)
	}
	// Nothing written, nothing committed.
	head := tgit(t, dir, "rev-parse", "HEAD")
	log.Reset()
	recordApply(c, "web", cliCommitter, &log)
	if tgit(t, dir, "rev-parse", "HEAD") != head || log.Len() != 0 {
		t.Errorf("committed again: %q", log.String())
	}
}
