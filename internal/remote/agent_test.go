// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A home with an encrypted and a plain key, and an agent of its own.
func keyHome(t *testing.T) (home, locked, plain string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("OpenSSH for Windows runs its agent as a service, not on a socket of our own")
	}
	for _, p := range []string{"ssh-agent", "ssh-add", "ssh-keygen"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Skip(p + " isn't installed")
		}
	}
	home = t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	locked, plain = filepath.Join(dir, "id_ed25519"), filepath.Join(dir, "plain")
	for path, pass := range map[string]string{locked: "right horse", plain: ""} {
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", pass, "-f", path).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v %s", err, out)
		}
	}
	sock, stop, err := StartAgent(filepath.Join(home, "agent"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	t.Setenv("SSH_AUTH_SOCK", sock)
	return home, locked, plain
}

func TestUnlock(t *testing.T) {
	home, locked, plain := keyHome(t)
	ctx := context.Background()
	if !Encrypted(ctx, locked) || Encrypted(ctx, plain) {
		t.Fatal("telling encrypted keys apart")
	}
	askpass := filepath.Join(home, "askpass")
	if err := os.WriteFile(askpass, []byte("#!/bin/sh\nif rm \"$"+AskpassOnceEnv+"\" 2>/dev/null; then printf '%s\\n' \"$"+AskpassEnv+"\"; else echo; fi\n"), 0o700); err != nil { //nolint:gosec // a test helper
		t.Fatal(err)
	}
	if err := Unlock(ctx, locked, "wrong", askpass, time.Minute); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("a wrong passphrase: %v", err)
	}
	if err := Unlock(ctx, "~/.ssh/id_ed25519", "right horse", askpass, time.Minute); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	out, _ := exec.Command("ssh-add", "-l").Output()
	if fp := fingerprint(ctx, locked); fp == "" || !strings.Contains(string(out), fp) {
		t.Errorf("the key isn't in the agent: %s", out)
	}
	// Only private keys in ~/.ssh.
	outside := filepath.Join(home, "elsewhere")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, ".ssh", "link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{outside, filepath.Join(home, ".ssh", "link"), locked + ".pub", "relative", "/etc/shadow"} {
		if _, err := UnlockableKey(p); err == nil {
			t.Errorf("%s was accepted", p)
		}
	}
}

func TestLockedKeyError(t *testing.T) {
	e := &LockedKeyError{Target: Target{User: "ada", Host: "vps"}, Keys: []string{"/home/ada/.ssh/id_rsa"}}
	if !strings.Contains(e.Error(), "id_rsa") || !strings.Contains(e.Error(), "passphrase") {
		t.Errorf("message: %s", e.Error())
	}
	var target *LockedKeyError
	if !errors.As(error(e), &target) {
		t.Error("errors.As")
	}
}
