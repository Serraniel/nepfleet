// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyIncoming(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("needs ssh-keygen")
	}
	dir := t.TempDir()
	keys := t.TempDir()
	newKey := func(name string) (priv, line string) {
		priv = filepath.Join(keys, name)
		if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", priv).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %v %s", err, out)
		}
		pub, err := os.ReadFile(priv + ".pub")
		if err != nil {
			t.Fatal(err)
		}
		f := strings.Fields(string(pub))
		return priv, name + "@example.org " + f[0] + " " + f[1]
	}
	mine, mineLine := newKey("me")
	other, _ := newKey("other")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.org"}, args...)...)
		// Not the person's git config: it may sign every commit (GPG).
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(msg, key string) string {
		if err := os.WriteFile(filepath.Join(dir, msg), []byte(msg), 0o600); err != nil {
			t.Fatal(err)
		}
		git("add", msg)
		args := []string{"commit", "-q", "-m", msg}
		if key != "" {
			args = append([]string{"-c", "gpg.format=ssh", "-c", "user.signingkey=" + key}, append(args, "-S")...)
		}
		git(args...)
		return git("rev-parse", "HEAD")
	}
	git("init", "-q")
	base := commit("base", "")
	signed := commit("signed", mine)
	if err := verifyIncoming(dir, []string{mineLine}, base, signed); err != nil {
		t.Errorf("a commit signed by an allowed key: %v", err)
	}
	byOther := commit("other", other)
	if err := verifyIncoming(dir, []string{mineLine}, base, byOther); !errors.Is(err, errUnsigned) {
		t.Errorf("signed by a key that isn't allowed: %v", err)
	}
	unsigned := commit("unsigned", "")
	if err := verifyIncoming(dir, []string{mineLine}, byOther, unsigned); !errors.Is(err, errUnsigned) {
		t.Errorf("unsigned: %v", err)
	}
}

func TestSSHSigned(t *testing.T) {
	ssh := "tree x\nauthor a\ngpgsig -----BEGIN SSH SIGNATURE-----\n U1NIU0lH\n -----END SSH SIGNATURE-----\n\nmsg\n"
	pgp := "tree x\nauthor a\ngpgsig -----BEGIN PGP SIGNATURE-----\n abc\n -----END PGP SIGNATURE-----\n\nmsg\n"
	inBody := "tree x\nauthor a\n\ngpgsig -----BEGIN SSH SIGNATURE-----\n"
	if !sshSigned(ssh) || sshSigned(pgp) || sshSigned(inBody) || sshSigned("tree x\n\nmsg") {
		t.Error("sshSigned")
	}
}
