// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// SSH runs with BatchMode, as nobody can answer a prompt there. A key with a
// passphrase therefore has to be in an SSH agent: when none runs, NepFleet
// starts its own, and unlocking a key adds it there for a while.

// AskpassEnv carries a passphrase to `nepfleet askpass`, the SSH_ASKPASS helper
// ssh-add runs, in that one child process's environment only.
const AskpassEnv = "NEPFLEET_ASKPASS" //nolint:gosec // the name of a variable, not a credential

// AskpassOnceEnv names a file askpass removes before answering: ssh-add asks
// again after a wrong passphrase, and gets an empty answer then, which ends it.
const AskpassOnceEnv = "NEPFLEET_ASKPASS_ONCE" //nolint:gosec // the name of a variable, not a credential

// KeyLifetime is how long an unlocked key stays in the agent.
const KeyLifetime = 8 * time.Hour

// LockedKeyError is a refused login where a key ssh would offer has a
// passphrase and isn't in the agent: unlocking it is the way in.
type LockedKeyError struct {
	Target Target
	Keys   []string
}

func (e *LockedKeyError) Error() string {
	return fmt.Sprintf("%s: SSH refused the login, and your key %s has a passphrase. Unlock it for NepFleet: the app asks for it, and on the command line run nepfleet key unlock", e.Target, strings.Join(e.Keys, ", "))
}

// ErrNotUnlockable is the one answer for any path that isn't a private key in
// ~/.ssh: the reason would tell a caller which files exist.
var ErrNotUnlockable = errors.New("only private keys directly in ~/.ssh can be unlocked")

// ErrWrongPassphrase means ssh-add refused the passphrase.
var ErrWrongPassphrase = errors.New("that passphrase doesn't unlock the key")

// AgentReachable says whether an SSH agent answers on sock.
func AgentReachable(sock string) bool {
	if sock == "" {
		return false
	}
	c, err := net.DialTimeout("unix", sock, time.Second) //nolint:gosec // a local agent socket of this user, not a network address
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// StartAgent runs an ssh-agent on a socket in dir, which only the owner may use,
// until stop is called.
func StartAgent(dir string) (sock string, stop func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	sock = filepath.Join(dir, "ssh-agent.sock")
	// Left by a daemon that ended without stopping it (killed): keep using it,
	// rather than leave it running with keys nobody can reach.
	if AgentReachable(sock) {
		return sock, func() {}, nil
	}
	_ = os.Remove(sock)
	cmd := exec.Command("ssh-agent", "-D", "-a", sock) //nolint:gosec // a fixed program, our own socket path
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("starting ssh-agent: %w", err)
	}
	for range 50 {
		if AgentReachable(sock) {
			return sock, func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait(); _ = os.Remove(sock) }, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return "", nil, errors.New("ssh-agent didn't start")
}

// identityFiles are the keys ssh would offer to t, from `ssh -G`, and the
// agent it would ask (IdentityAgent), if the config names one.
func (s SSH) identityFiles(ctx context.Context, t Target) (files []string, agent string) {
	args := []string{"-G"}
	if s.ConfigFile != "" {
		args = append(args, "-F", s.ConfigFile)
	}
	if t.Port != 0 {
		args = append(args, "-p", fmt.Sprint(t.Port))
	}
	if t.User != "" {
		args = append(args, "-l", t.User)
	}
	out, err := exec.CommandContext(ctx, "ssh", append(args, "--", t.Host)...).Output() //nolint:gosec // names checked in ParseTarget, after "--"
	if err != nil {
		return nil, ""
	}
	home, _ := os.UserHomeDir()
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), " ")
		if ok && k == "identityagent" {
			agent = v
			continue
		}
		if !ok || k != "identityfile" {
			continue
		}
		if rest, ok := strings.CutPrefix(v, "~/"); ok && home != "" {
			v = filepath.Join(home, rest)
		}
		if fi, err := os.Stat(v); err == nil && fi.Mode().IsRegular() && !slices.Contains(files, v) {
			files = append(files, v)
		}
	}
	return files, agent
}

// Encrypted says whether a private key has a passphrase.
func Encrypted(ctx context.Context, path string) bool {
	cmd := exec.CommandContext(ctx, "ssh-keygen", "-y", "-P", "", "-f", path) //nolint:gosec // a key path, as one argument
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return err != nil && strings.Contains(strings.ToLower(stderr.String()), "passphrase")
}

// inAgent lists the fingerprints the agent holds.
func inAgent(ctx context.Context) []string {
	out, _ := exec.CommandContext(ctx, "ssh-add", "-l").Output()
	var fps []string
	for line := range strings.Lines(string(out)) {
		if f := strings.Fields(line); len(f) >= 2 {
			fps = append(fps, f[1])
		}
	}
	return fps
}

// fingerprint is a key's, from its .pub file next to it, if there is one.
func fingerprint(ctx context.Context, path string) string {
	out, err := exec.CommandContext(ctx, "ssh-keygen", "-l", "-f", path+".pub").Output() //nolint:gosec // a key path, as one argument
	if f := strings.Fields(string(out)); err == nil && len(f) >= 2 {
		return f[1]
	}
	return ""
}

// lockedKeys are the keys ssh would offer to t that have a passphrase, aren't
// in the agent, and can be unlocked here (in ~/.ssh). None when the SSH config
// names another agent (IdentityAgent), as keys added to ours wouldn't be used.
func (s SSH) lockedKeys(ctx context.Context, t Target) []string {
	files, agent := s.identityFiles(ctx, t)
	if agent != "" && agent != "SSH_AUTH_SOCK" && agent != "$SSH_AUTH_SOCK" {
		return nil
	}
	loaded := inAgent(ctx)
	var locked []string
	for _, f := range files {
		if _, err := UnlockableKey(f); err != nil {
			continue
		}
		if fp := fingerprint(ctx, f); fp != "" && slices.Contains(loaded, fp) {
			continue
		}
		if Encrypted(ctx, f) {
			locked = append(locked, f)
		}
	}
	return locked
}

// UnlockableKey checks that path is a private key in the user's ~/.ssh, the
// only keys unlocking takes, and returns it resolved.
func UnlockableKey(path string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(home, ".ssh"))
	if err != nil {
		return "", ErrNotUnlockable
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		path = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(path) {
		return "", ErrNotUnlockable
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", ErrNotUnlockable
	}
	if filepath.Dir(resolved) != dir {
		return "", ErrNotUnlockable
	}
	if fi, err := os.Stat(resolved); err != nil || !fi.Mode().IsRegular() || strings.HasSuffix(resolved, ".pub") {
		return "", ErrNotUnlockable
	}
	return resolved, nil
}

// Unlock adds a key to the agent for lifetime, with its passphrase given to
// ssh-add through askpass (a program answering with $NEPFLEET_ASKPASS): never
// as an argument, and only in that child's environment.
func Unlock(ctx context.Context, path, passphrase, askpass string, lifetime time.Duration) error {
	key, err := UnlockableKey(path)
	if err != nil {
		return err
	}
	if strings.ContainsAny(passphrase, "\n\x00") || passphrase == "" {
		return ErrWrongPassphrase
	}
	if !AgentReachable(os.Getenv("SSH_AUTH_SOCK")) {
		return errors.New("no SSH agent runs: start one (or the NepFleet app), then try again")
	}
	once, err := os.CreateTemp("", "nepfleet-askpass-")
	if err != nil {
		return err
	}
	_ = once.Close()
	defer func() { _ = os.Remove(once.Name()) }()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh-add", "-t", fmt.Sprint(int(lifetime.Seconds())), key) //nolint:gosec // a checked key path, as one argument
	cmd.Env = append(os.Environ(), "SSH_ASKPASS="+askpass, "SSH_ASKPASS_REQUIRE=force",
		AskpassEnv+"="+passphrase, AskpassOnceEnv+"="+once.Name())
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		// askpass answered (it removes the file then), and ssh-add still failed:
		// it asked again, so the passphrase was wrong.
		_, statErr := os.Stat(once.Name())
		if errors.Is(statErr, os.ErrNotExist) || bytes.Contains(bytes.ToLower(out), []byte("bad passphrase")) {
			return ErrWrongPassphrase
		}
		return fmt.Errorf("ssh-add: %s", Clean(strings.ReplaceAll(string(out), passphrase, "••••••")))
	}
	return nil
}
