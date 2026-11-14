// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/remote"
)

// askpass answers once with the passphrase, then empty, so ssh-add stops
// asking after a wrong one.
func TestAskpass(t *testing.T) {
	once := filepath.Join(t.TempDir(), "once")
	if err := os.WriteFile(once, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(remote.AskpassEnv, "s3cret")
	t.Setenv(remote.AskpassOnceEnv, once)
	var out bytes.Buffer
	if err := cmdAskpass(&out); err != nil || out.String() != "s3cret\n" {
		t.Fatalf("first: %q %v", out.String(), err)
	}
	out.Reset()
	if err := cmdAskpass(&out); err != nil || out.String() != "\n" {
		t.Errorf("second: %q %v", out.String(), err)
	}
	t.Setenv(remote.AskpassEnv, "")
	if err := cmdAskpass(&out); err == nil {
		t.Error("without a passphrase it must fail")
	}
}

// TestMain lets this test binary stand in for nepfleet as SSH_ASKPASS: ssh-add
// runs it with its prompt as the only argument, as it runs the real binary.
func TestMain(m *testing.M) {
	if os.Getenv(remote.AskpassOnceEnv) != "" {
		if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The whole way: a real ssh-add asks this binary for the passphrase.
func TestUnlockThroughSSHAdd(t *testing.T) {
	for _, p := range []string{"ssh-agent", "ssh-add", "ssh-keygen"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Skip(p + " isn't installed")
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	key := filepath.Join(home, ".ssh", "id_ed25519")
	if err := os.Mkdir(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "right horse", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	sock, stop, err := remote.StartAgent(filepath.Join(home, "a"))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	t.Setenv("SSH_AUTH_SOCK", sock)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := remote.Unlock(ctx, key, "wrong", self, time.Minute); !errors.Is(err, remote.ErrWrongPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	if err := remote.Unlock(ctx, key, "right horse", self, time.Minute); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if out, _ := exec.Command("ssh-add", "-l").Output(); !strings.Contains(string(out), "ED25519") {
		t.Errorf("not in the agent: %s", out)
	}
}
