// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

func findMirror(t *testing.T, st []api.MirrorStatus, id string) api.MirrorStatus {
	t.Helper()
	for _, m := range st {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("no mirror %s in %+v", id, st)
	return api.MirrorStatus{}
}

func TestMirrorSetupAPI(t *testing.T) {
	c, dir := secretFleet(t)
	bare := filepath.Join(t.TempDir(), "mirror.git")
	tgit(t, filepath.Dir(bare), "init", "-q", "--bare", bare)

	// A local mirror: no key, and the first push lands.
	added, err := addMirrorAPI(c, api.MirrorAdd{URL: bare}, api.MirrorLocal, "ci")
	if err != nil {
		t.Fatal(err)
	}
	if added.Mirror.Auth != "host" || added.Mirror.PublicKey != "" || added.Mirror.ID != api.MirrorID(bare) {
		t.Errorf("local mirror: %+v", added.Mirror)
	}
	if head := strings.TrimSpace(tgit(t, dir, "rev-parse", "HEAD")); head != added.Commit {
		t.Errorf("commit %s, HEAD %s", added.Commit, head)
	}
	if _, err := addMirrorAPI(c, api.MirrorAdd{URL: bare}, api.MirrorLocal, "ci"); !errors.As(err, new(api.InvalidError)) {
		t.Errorf("adding it twice: %v", err)
	}
	// The fleet repository's own hooks don't run: what's in it doesn't get
	// to execute on a push.
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(dir, ".git", "hooks", "pre-push")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0o700); err != nil { //nolint:gosec // a test hook
		t.Fatal(err)
	}
	st, err := mirrorAPI(t.Context(), c, true)
	if err != nil {
		t.Fatal(err)
	}
	if m := findMirror(t, st, api.MirrorID(bare)); m.OK.IsZero() || m.Error != "" {
		t.Errorf("first push: %+v", m)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the fleet repository's pre-push hook ran")
	}
	branch := strings.TrimSpace(tgit(t, dir, "branch", "--show-current"))
	if got, want := strings.TrimSpace(tgit(t, bare, "rev-parse", "refs/heads/"+branch)), strings.TrimSpace(tgit(t, dir, "rev-parse", "HEAD")); got != want {
		t.Errorf("mirror main = %s, want %s", got, want)
	}

	// An SSH mirror gets a deploy key: stored encrypted, its public half shown.
	const sshURL = "git@git.example.test:me/fleet.git"
	added, err = addMirrorAPI(c, api.MirrorAdd{URL: sshURL}, api.MirrorSSH, "ci")
	if err != nil {
		t.Fatal(err)
	}
	pub := added.Mirror.PublicKey
	if added.Mirror.Auth != "deploy-key" || !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Fatalf("ssh mirror: %+v", added.Mirror)
	}
	for _, f := range []string{"fleet.yaml", fleet.FleetSecretsFile} {
		raw, _ := os.ReadFile(filepath.Join(dir, f)) //nolint:gosec // the test's fleet
		if strings.Contains(string(raw), "PRIVATE KEY") {
			t.Fatalf("%s holds the private key in plain text", f)
		}
	}

	// The push uses that key, from a file that is gone afterwards.
	fake := filepath.Join(t.TempDir(), "fake-ssh")
	used := filepath.Join(t.TempDir(), "used-key")
	script := "#!/bin/sh\nwhile [ $# -gt 0 ]; do if [ \"$1\" = -i ]; then cp \"$2\" '" + used + "'; fi; shift; done\necho 'Permission denied (publickey).' >&2\nexit 255\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil { //nolint:gosec // a test program
		t.Fatal(err)
	}
	old := mirrorSSH
	mirrorSSH = fake
	t.Cleanup(func() { mirrorSSH = old })
	st, err = mirrorAPI(t.Context(), c, true)
	if err != nil {
		t.Fatal(err)
	}
	m := findMirror(t, st, api.MirrorID(sshURL))
	if m.Error == "" || !strings.Contains(m.Error, "deploy key") || m.PublicKey != pub || m.Auth != "deploy-key" {
		t.Errorf("push without the key on the forge: %+v", m)
	}
	key, err := os.ReadFile(used) //nolint:gosec // the test's file
	if err != nil {
		t.Fatalf("ssh didn't get the deploy key: %v", err)
	}
	if publicOf(string(key)) != pub {
		t.Error("ssh got another key than the deploy key")
	}
	if left, _ := filepath.Glob(filepath.Join(c.stateDir, "mirror-key-*")); len(left) != 0 {
		t.Errorf("the key file stayed: %v", left)
	}

	// A forge that never answers doesn't hold the push (and the API lock):
	// the context ends it, and the key file goes all the same.
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 60\n"), 0o700); err != nil { //nolint:gosec // a test program
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	start := time.Now()
	st, err = mirrorAPI(ctx, c, true)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("a stalled push took %s", took)
	}
	if m := findMirror(t, st, api.MirrorID(sshURL)); m.Error == "" {
		t.Errorf("a stalled push: %+v", m)
	}
	if left, _ := filepath.Glob(filepath.Join(c.stateDir, "mirror-key-*")); len(left) != 0 {
		t.Errorf("the key file stayed after a stalled push: %v", left)
	}

	// An https mirror with a token: stored encrypted, never in fleet.yaml.
	const httpsURL = "https://git.example.test/me/fleet.git"
	added, err = addMirrorAPI(c, api.MirrorAdd{URL: httpsURL, Token: "tok3n"}, api.MirrorHTTPS, "ci")
	if err != nil || added.Mirror.Auth != "token" {
		t.Fatalf("https mirror: %+v %v", added, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "fleet.yaml")); strings.Contains(string(raw), "tok3n") { //nolint:gosec // the test's fleet
		t.Fatal("fleet.yaml holds the token")
	}
	e, _ := c.open()
	// The header goes only to the mirror's URL.
	if env, done, err := newMirrorSecrets(e).pushEnv(c.stateDir, httpsURL); err != nil ||
		!strings.Contains(strings.Join(env, "\n"), "GIT_CONFIG_VALUE_0=Authorization: Basic ") ||
		!slices.Contains(env, "GIT_CONFIG_KEY_0=http."+httpsURL+".extraHeader") {
		t.Errorf("https push env: %v %v", env, err)
	} else {
		done()
	}

	// Removing takes the secrets with it.
	for _, u := range []string{sshURL, httpsURL, bare} {
		if _, err := removeMirrorAPI(c, api.MirrorID(u), "ci"); err != nil {
			t.Fatal(err)
		}
	}
	e, _ = c.open()
	if len(e.fleet.Spec.Mirrors) != 0 {
		t.Errorf("mirrors left: %v", e.fleet.Spec.Mirrors)
	}
	keys, _ := secretFileKeys(filepath.Join(dir, fleet.FleetSecretsFile))
	for _, k := range keys {
		if strings.HasPrefix(k, "MIRROR_") {
			t.Errorf("secret %s stayed", k)
		}
	}
	if _, err := removeMirrorAPI(c, api.MirrorID(bare), "ci"); !errors.Is(err, api.ErrNoMirror) {
		t.Errorf("removing again: %v", err)
	}
	if st := tgit(t, dir, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("left uncommitted: %s", st)
	}
}

// serve's start removes key files a dead push left, not one in use.
func TestSweepMirrorKeys(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	stale, fresh := filepath.Join(dir, "mirror-key-1"), filepath.Join(dir, "mirror-key-2")
	other := filepath.Join(dir, "mirror.json")
	for _, f := range []string{stale, fresh, other} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := now.Add(-time.Hour)
	for _, f := range []string{stale, other} {
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}
	sweepMirrorKeys(dir, now)
	if _, err := os.Stat(stale); err == nil {
		t.Error("a stale key file stayed")
	}
	for _, f := range []string{fresh, other} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s went: %v", filepath.Base(f), err)
		}
	}
}

// The app offers its mirror setup for a Repository mirror check that isn't OK.
func TestMirrorAction(t *testing.T) {
	c, _ := secretFleet(t)
	e, err := c.open()
	if err != nil {
		t.Fatal(err)
	}
	action := doctorAction(c)
	for _, ch := range backupChecks(e, c.stateDir, "") {
		if ch.Name == "Repository mirror" {
			if ch.Detail != noMirror || action(ch) != MirrorAction {
				t.Errorf("no mirror: %+v, action %q", ch, action(ch))
			}
			return
		}
	}
	t.Fatal("no Repository mirror check")
}
