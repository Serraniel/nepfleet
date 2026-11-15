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
	if err := verifyIncoming(dir, []string{mineLine}, nil, base, signed); err != nil {
		t.Errorf("a commit signed by an allowed key: %v", err)
	}
	byOther := commit("other", other)
	if err := verifyIncoming(dir, []string{mineLine}, nil, base, byOther); !errors.Is(err, errUnsigned) {
		t.Errorf("signed by a key that isn't allowed: %v", err)
	}
	unsigned := commit("unsigned", "")
	if err := verifyIncoming(dir, []string{mineLine}, nil, byOther, unsigned); !errors.Is(err, errUnsigned) {
		t.Errorf("unsigned: %v", err)
	}
}

func TestSignatureKind(t *testing.T) {
	for raw, want := range map[string]string{
		"tree x\nauthor a\ngpgsig -----BEGIN SSH SIGNATURE-----\n U1NIU0lH\n -----END SSH SIGNATURE-----\n\nmsg\n": "ssh",
		"tree x\nauthor a\ngpgsig -----BEGIN PGP SIGNATURE-----\n abc\n -----END PGP SIGNATURE-----\n\nmsg\n":      "pgp",
		"tree x\nauthor a\ngpgsig-sha256 -----BEGIN PGP SIGNATURE-----\n abc\n\nmsg\n":                             "pgp",
		"tree x\nauthor a\ngpgsig -----BEGIN SIGNED MESSAGE-----\n abc\n\nmsg\n":                                   "",
		"tree x\nauthor a\n\ngpgsig -----BEGIN SSH SIGNATURE-----\n":                                               "",
		"tree x\n\nmsg": "",
		// Two signatures: git verifies one of them by the repository's format.
		"tree x\ngpgsig-sha256 -----BEGIN SSH SIGNATURE-----\n a\n -----END SSH SIGNATURE-----\ngpgsig -----BEGIN PGP SIGNATURE-----\n b\n\nmsg\n": "",
	} {
		if got := signatureKind(raw); got != want {
			t.Errorf("%q: %q, want %q", raw, got, want)
		}
	}
}

// OpenPGP signatures count only by the listed keys, never by keys the
// machine's own keyring knows.
func TestVerifyIncomingGPG(t *testing.T) {
	for _, tool := range []string{"gpg", "gpgconf"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("needs " + tool)
		}
	}
	dir := t.TempDir()
	// Key homes outside t.TempDir's long paths: gpg-agent's socket path is limited.
	homes, err := os.MkdirTemp("", "nf-gpg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		removeKeyring(filepath.Join(homes, "me"))
		removeKeyring(filepath.Join(homes, "other"))
		_ = os.RemoveAll(homes)
	})
	newKey := func(name string) (home, armored string) {
		home = filepath.Join(homes, name)
		if err := os.Mkdir(home, 0o700); err != nil {
			t.Fatal(err)
		}
		gpg := func(args ...string) []byte {
			out, err := exec.Command("gpg", append([]string{"--homedir", home, "--batch", "--no-tty"}, args...)...).CombinedOutput()
			if err != nil {
				t.Fatalf("gpg %v: %v %s", args, err, out)
			}
			return out
		}
		gpg("--passphrase", "", "--quick-gen-key", name+" <"+name+"@example.test>", "ed25519", "sign", "never")
		out, err := exec.Command("gpg", "--homedir", home, "--batch", "--armor", "--export", name+"@example.test").Output()
		if err != nil {
			t.Fatal(err)
		}
		return home, string(out)
	}
	mine, mineKey := newKey("me")
	other, _ := newKey("other")
	git := func(env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.test"}, args...)...)
		cmd.Env = append(append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(msg, home, signer string) string {
		if err := os.WriteFile(filepath.Join(dir, msg), []byte(msg), 0o600); err != nil {
			t.Fatal(err)
		}
		git(nil, "add", msg)
		if home == "" {
			git(nil, "commit", "-q", "-m", msg)
		} else {
			git([]string{"GNUPGHOME=" + home}, "-c", "user.signingkey="+signer+"@example.test", "commit", "-q", "-S", "-m", msg)
		}
		return git(nil, "rev-parse", "HEAD")
	}
	git(nil, "init", "-q")
	base := commit("base", "", "")
	signed := commit("signed", mine, "me")
	if err := verifyIncoming(dir, nil, []string{mineKey}, base, signed); err != nil {
		t.Errorf("signed by a listed key: %v", err)
	}
	byOther := commit("other", other, "other")
	// The machine's own keyring knows the other key: it must not count.
	t.Setenv("GNUPGHOME", other)
	if err := verifyIncoming(dir, nil, []string{mineKey}, signed, byOther); !errors.Is(err, errUnsigned) {
		t.Errorf("signed by a key that isn't listed: %v", err)
	}
	if err := verifyIncoming(dir, []string{"me@example.test ssh-ed25519 AAAA"}, nil, base, signed); !errors.Is(err, errUnsigned) {
		t.Errorf("an OpenPGP signature with only SSH signers: %v", err)
	}
	unsigned := commit("unsigned", "", "")
	if err := verifyIncoming(dir, nil, []string{mineKey}, byOther, unsigned); !errors.Is(err, errUnsigned) {
		t.Errorf("unsigned: %v", err)
	}
	if _, err := gpgKeyring([]string{"-----BEGIN PGP PUBLIC KEY BLOCK-----\nnonsense\n-----END PGP PUBLIC KEY BLOCK-----"}); err == nil {
		t.Error("a keyring from no key")
	}
}
