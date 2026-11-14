// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/term"

	"github.com/Serraniel/nepfleet/internal/remote"
)

// cmdAskpass is the SSH_ASKPASS helper ssh-add runs when NepFleet unlocks a
// key: it answers with the passphrase NepFleet put in its environment, and
// nothing else. Not for people to run.
func cmdAskpass(stdout io.Writer) error {
	p := os.Getenv(remote.AskpassEnv)
	if p == "" {
		return errors.New("askpass: no passphrase was given to it")
	}
	// Once only: asked again means the passphrase was wrong, and an empty
	// answer makes ssh-add give up instead of asking forever.
	if once := os.Getenv(remote.AskpassOnceEnv); once == "" || os.Remove(once) != nil {
		_, err := fmt.Fprintln(stdout)
		return err
	}
	_, err := fmt.Fprintln(stdout, p)
	return err
}

// ensureAgent makes sure an SSH agent answers, starting one for this run when
// none does; stop ends it.
func ensureAgent() (stop func(), err error) {
	if remote.AgentReachable(os.Getenv("SSH_AUTH_SOCK")) {
		return func() {}, nil
	}
	dir, err := os.MkdirTemp("", "nepfleet-agent-")
	if err != nil {
		return nil, err
	}
	sock, stopAgent, err := remote.StartAgent(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	_ = os.Setenv("SSH_AUTH_SOCK", sock)
	return func() { stopAgent(); _ = os.RemoveAll(dir) }, nil
}

// cliKeyLifetime is how long the host commands keep a key unlocked: they may
// use your own agent, and need it only for their run.
const cliKeyLifetime = time.Hour

// unlockKeys asks for each key's passphrase on the terminal, without echo, and
// adds the key to the agent for lifetime.
func unlockKeys(ctx context.Context, keys []string, lifetime time.Duration, stdout io.Writer) error {
	fd := int(os.Stdin.Fd()) //nolint:gosec // file descriptors fit in int
	if !term.IsTerminal(fd) {
		return errors.New("a key needs its passphrase, and there is no terminal to ask on: unlock it in the app, or run nepfleet key unlock in a terminal")
	}
	askpass, err := os.Executable()
	if err != nil {
		return err
	}
	for _, k := range keys {
		for attempt := 0; ; attempt++ {
			fmt.Fprintf(stdout, "Passphrase for %s: ", k)
			b, err := readHidden(fd)
			fmt.Fprintln(stdout)
			if err != nil {
				return err
			}
			err = remote.Unlock(ctx, k, string(b), askpass, lifetime)
			if errors.Is(err, remote.ErrWrongPassphrase) && attempt < 2 {
				fmt.Fprintln(stdout, "That doesn't unlock it; try again.")
				continue
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s is unlocked for %s\n", k, lifetime)
			break
		}
	}
	return nil
}

// probeUnlocking probes t, and when a key with a passphrase is why SSH refused,
// asks for it on the terminal, unlocks it into an agent and tries again. stop
// ends an agent started for this run.
func probeUnlocking(ctx context.Context, ssh remote.SSH, t remote.Target, stdout io.Writer) (*remote.Report, func(), error) {
	stop := func() {}
	r, err := remote.Probe(ctx, ssh.Run, t)
	var locked *remote.LockedKeyError
	if !errors.As(err, &locked) {
		return r, stop, err
	}
	if stop, err = ensureAgent(); err != nil {
		return nil, func() {}, err
	}
	if err := unlockKeys(ctx, locked.Keys, cliKeyLifetime, stdout); err != nil {
		return nil, stop, err
	}
	r, err = remote.Probe(ctx, ssh.Run, t)
	return r, stop, err
}

// cmdKeyUnlock: nepfleet key unlock [key...] adds SSH keys with a passphrase to
// the SSH agent, for the app's daemon and other programs using that agent.
func cmdKeyUnlock(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("key unlock", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if !remote.AgentReachable(os.Getenv("SSH_AUTH_SOCK")) {
		return errors.New("no SSH agent runs here: the NepFleet app's daemon starts its own, and unlocks keys from the app; or start one with eval \"$(ssh-agent)\"")
	}
	keys := fs.Args()
	if len(keys) == 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa", "id_ed25519_sk", "id_ecdsa_sk"} {
			if p := filepath.Join(home, ".ssh", n); remote.Encrypted(context.Background(), p) {
				keys = append(keys, p)
			}
		}
		if len(keys) == 0 {
			return errors.New("none of your usual keys in ~/.ssh has a passphrase; name the key to unlock")
		}
	}
	return unlockKeys(context.Background(), keys, remote.KeyLifetime, stdout)
}
