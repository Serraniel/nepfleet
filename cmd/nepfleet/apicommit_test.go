// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

func tgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "user.name=Server", "-c", "user.email=server@example.test"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A fleet with two versions of a plain stack, as a git repository with its own
// identity and no signing (tests must not need the developer's key).
func rollbackFleet(t *testing.T) (common, string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := fleet.Init(dir); err != nil {
		t.Fatal(err)
	}
	st := filepath.Join(dir, "stacks", "web")
	if err := os.MkdirAll(st, 0o750); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(st, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("stack.yaml", "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: web}\nspec: {target: local}\n")
	write("compose.yaml", "services:\n  web:\n    image: nginx:1.28-alpine\n")
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		tgit(t, dir, "init", "-q")
	}
	for _, kv := range [][2]string{{"commit.gpgsign", "false"}, {"user.name", "Server"}, {"user.email", "server@example.test"}} {
		tgit(t, dir, "config", kv[0], kv[1])
	}
	tgit(t, dir, "add", "-A")
	tgit(t, dir, "commit", "-qm", "one", "--no-verify")
	old := tgit(t, dir, "rev-parse", "HEAD")
	write("compose.yaml", "services:\n  web:\n    image: nginx:1.29-alpine\n")
	write("added-later.txt", "x\n")
	tgit(t, dir, "add", "-A")
	tgit(t, dir, "commit", "-qm", "two", "--no-verify")
	return common{fleetDir: dir, stateDir: t.TempDir(), host: "h", offline: true}, dir, old
}

func allowAll(*deploy.Plan) error { return nil }

func TestRestoreStack(t *testing.T) {
	c, dir, old := rollbackFleet(t)
	var log bytes.Buffer
	author := apiAuthor("ci <bot>")
	if err := restoreStack(c, "web", old, author, allowAll, &log); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "stacks/web/compose.yaml"))
	if !strings.Contains(string(raw), "1.28") {
		t.Errorf("not restored: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "stacks/web/added-later.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a file added since must go: %v", err)
	}
	if got := tgit(t, dir, "log", "-1", "--format=%an <%ae>|%cn|%s"); !strings.HasPrefix(got, "ci bot <ci-bot@tokens.nepfleet.invalid>|Server|revert(web): roll back to") {
		t.Errorf("commit: %s", got)
	}
	if s := tgit(t, dir, "status", "--porcelain"); s != "" {
		t.Errorf("left dirty: %s", s)
	}
}

func TestRestoreStackRefusalLeavesEverything(t *testing.T) {
	c, dir, old := rollbackFleet(t)
	head := tgit(t, dir, "rev-parse", "HEAD")
	var log bytes.Buffer
	refuse := func(*deploy.Plan) error { return api.ErrNeedsConfirm }
	if err := restoreStack(c, "web", old, apiAuthor("ci"), refuse, &log); !errors.Is(err, api.ErrNeedsConfirm) {
		t.Fatalf("%v", err)
	}
	if got := tgit(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved: %s", got)
	}
	if s := tgit(t, dir, "status", "--porcelain"); s != "" {
		t.Errorf("the tree must be as it was:\n%s", s)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "stacks/web/compose.yaml"))
	if !strings.Contains(string(raw), "1.29") {
		t.Errorf("files must be back: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, "stacks/web/added-later.txt")); err != nil {
		t.Errorf("a file removed for the attempt must be back: %v", err)
	}
}

func TestRestoreStackRefusesUncommitted(t *testing.T) {
	c, dir, old := rollbackFleet(t)
	p := filepath.Join(dir, "stacks/web/compose.yaml")
	if err := os.WriteFile(p, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if err := restoreStack(c, "web", old, apiAuthor("ci"), allowAll, &log); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("%v", err)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != "services: {}\n" {
		t.Errorf("someone's edit was lost: %s", raw)
	}
}

func TestStartRollbackOnlyOwnHistory(t *testing.T) {
	c, dir, _ := rollbackFleet(t)
	other := tgit(t, dir, "hash-object", "-t", "tree", "/dev/null")
	if _, err := startRollback(t.Context(), c, "web", other, false, "ci"); !errors.Is(err, api.ErrNotInHistory) {
		t.Errorf("a commit outside the stack's history: %v", err)
	}
	if _, err := startRollback(t.Context(), c, "nope", other, false, "ci"); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("unknown stack: %v", err)
	}
	if !apiMu.TryLock() {
		t.Fatal("a refused rollback must free the lock")
	}
	apiMu.Unlock()
}

func TestAPIAuthor(t *testing.T) {
	for by, want := range map[string]string{
		"ci":      "ci <ci@tokens.nepfleet.invalid>",
		"My Bot":  "My Bot <my-bot@tokens.nepfleet.invalid>",
		"a<b>\nc": "ab c <ab-c@tokens.nepfleet.invalid>",
		"":        "token <token@tokens.nepfleet.invalid>",
	} {
		if got := apiAuthor(by); got != want {
			t.Errorf("%q: %q", by, got)
		}
	}
}
