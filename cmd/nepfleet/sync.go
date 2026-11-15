// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/deploy"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/notify"
)

// Git sync keeps the server's fleet repository at its origin's commit: on a
// timer (serve --sync) or a forge's webhook it fetches, fast-forwards only, and
// applies the changed stacks set to apply: auto. The others wait for a person.

// syncer runs one sync at a time and remembers how the last one went.
type syncer struct {
	c        common
	interval time.Duration
	log      io.Writer
	kick     chan struct{}

	mu     sync.Mutex
	status api.SyncStatus
}

func newSyncer(c common, interval time.Duration, log io.Writer) *syncer {
	s := &syncer{c: c, interval: interval, log: log, kick: make(chan struct{}, 1)}
	s.status.Enabled = true
	if interval > 0 {
		s.status.Interval = interval.String()
	}
	return s
}

// Now asks for a sync soon; a sync already asked for counts once.
func (s *syncer) Now() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Status is how the last sync went.
func (s *syncer) Status() api.SyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	st.Applied = slices.Clone(st.Applied)
	st.Failed = slices.Clone(st.Failed)
	st.Pending = slices.Clone(st.Pending)
	return st
}

// run syncs on the timer and on Now, until ctx ends.
func (s *syncer) run(ctx context.Context) {
	var tick <-chan time.Time
	if s.interval > 0 {
		t := time.NewTicker(s.interval)
		defer t.Stop()
		tick = t.C
		s.Now() // once at start, too
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
		case <-s.kick:
		}
		s.once(ctx)
	}
}

// pulled is what a fast-forward brought in.
type pulled struct {
	from, to string
	stacks   []string
}

var errDiverged = errors.New("the fleet repository has commits that origin doesn't: sync only fast-forwards, so it stopped; push or reset them")

// pullFleet fast-forwards the fleet repository to its upstream, and names the
// stacks the new commits changed. A change to fleet.yaml counts for all.
func pullFleet(ctx context.Context, e *env) (pulled, error) {
	dir := e.fleet.Dir
	git := func(args ...string) (string, error) {
		out, err := gitOut(dir, args...)
		return strings.TrimSpace(out), err
	}
	if _, err := git("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err != nil {
		return pulled{}, errors.New("the fleet repository's branch tracks no upstream (git branch --set-upstream-to origin/main)")
	}
	if st, _ := git("status", "--porcelain", "--untracked-files=no"); st != "" {
		return pulled{}, errors.New("the fleet repository has uncommitted changes on the server: commit or discard them, then sync")
	}
	if ctx.Err() != nil {
		return pulled{}, ctx.Err()
	}
	if _, err := git("fetch", "--quiet"); err != nil {
		return pulled{}, err
	}
	from, err := git("rev-parse", "HEAD")
	if err != nil {
		return pulled{}, err
	}
	to, err := git("rev-parse", "@{u}")
	if err != nil {
		return pulled{}, err
	}
	if from == to {
		return pulled{from: from, to: to}, nil
	}
	if _, err := git("merge-base", "--is-ancestor", "HEAD", to); err != nil {
		return pulled{from: from}, errDiverged
	}
	// With requireSigned, every incoming commit needs a signature by a key
	// the repository allowed before this pull: a pushed commit can't
	// switch the check off or add its own key without one.
	if e.fleet.Spec.Sync.RequireSigned {
		if err := verifyIncoming(dir, e.fleet.Spec.Sync.AllowedSigners, e.fleet.Spec.Sync.AllowedGPGKeys, from, to); err != nil {
			return pulled{from: from}, err
		}
	}
	// The commit checked above, not @{u} again: something else may fetch
	// meanwhile.
	if _, err := git("merge", "--ff-only", "--quiet", to); err != nil {
		return pulled{from: from}, err
	}
	changed, err := git("diff", "--name-only", from, to)
	if err != nil {
		return pulled{from: from, to: to}, err
	}
	all, err := e.fleet.Stacks()
	if err != nil {
		return pulled{from: from, to: to}, err
	}
	everything := false
	touched := map[string]bool{}
	for _, f := range strings.Split(changed, "\n") {
		if f == "fleet.yaml" || f == fleet.FleetSecretsFile {
			everything = true
		}
		for _, s := range all {
			if rel := relTo(dir, s.Dir) + "/"; strings.HasPrefix(f, rel) {
				touched[s.Metadata.Name] = true
			}
		}
	}
	var names []string
	for _, s := range all {
		if everything || touched[s.Metadata.Name] {
			names = append(names, s.Metadata.Name)
		}
	}
	sort.Strings(names)
	return pulled{from: from, to: to, stacks: names}, nil
}

var errNeedsPerson = errors.New("destructive changes wait for a person")

// once pulls and applies what changed. A running apply of the API makes it
// wait for the next turn.
func (s *syncer) once(ctx context.Context) {
	if !apiMu.TryLock() {
		return
	}
	defer apiMu.Unlock()
	e, err := s.c.open()
	if err != nil {
		s.finish(api.SyncStatus{Error: err.Error()})
		return
	}
	p, err := pullFleet(ctx, e)
	st := api.SyncStatus{Commit: p.to}
	if err != nil {
		st.Error = err.Error()
		if st.Commit == "" {
			st.Commit = p.from
		}
		s.finish(st)
		if errors.Is(err, errDiverged) {
			s.emit(e, notify.Event{Name: "sync.failed", Severity: notify.Warning, Title: "Fleet sync stopped", Message: err.Error()})
		}
		return
	}
	for _, name := range p.stacks {
		stack, err := e.fleet.LoadStack(name)
		if err != nil {
			st.Failed = append(st.Failed, name)
			continue
		}
		if stack.Spec.Target != "local" && stack.Spec.Target != e.host {
			continue // another host's NepFleet applies it
		}
		if stack.Spec.Apply != "auto" {
			st.Pending = append(st.Pending, name)
			s.emit(e, notify.Event{Name: "sync.pending", Severity: notify.Info, Stack: name,
				Title: name + " changed", Message: "Pulled " + short(p.to) + ": review its plan and apply it."})
			continue
		}
		fmt.Fprintf(s.log, "sync: applying %s at %s\n", name, short(p.to))
		o := applyOptions{yes: true, wait: applyWait, check: func(pl *deploy.Plan) error {
			if len(pl.Destructive) > 0 {
				return errNeedsPerson
			}
			return nil
		}}
		err = runApply(ctx, s.c, []string{name}, o, s.log, s.log)
		switch {
		case errors.Is(err, errNeedsPerson):
			st.Pending = append(st.Pending, name)
			s.emit(e, notify.Event{Name: "sync.pending", Severity: notify.Warning, Stack: name,
				Title: name + " has destructive changes", Message: "Pulled " + short(p.to) + ": its plan removes something, so it waits for a person to confirm."})
		case err != nil:
			st.Failed = append(st.Failed, name)
			s.emit(e, notify.Event{Name: "sync.failed", Severity: notify.Error, Stack: name,
				Title: name + " failed to apply", Message: cleanReason(err, e.fleet.Dir)})
		default:
			st.Applied = append(st.Applied, name)
		}
	}
	s.finish(st)
}

func (s *syncer) finish(st api.SyncStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.Enabled, st.Interval, st.Last = s.status.Enabled, s.status.Interval, time.Now().UTC()
	if st.Commit == "" {
		st.Commit = s.status.Commit
	}
	s.status = st
}

func (s *syncer) emit(e *env, ev notify.Event) {
	n, err := e.notifier()
	if err != nil {
		n = nil
	}
	e.emit(n, s.log, ev)
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// verifyIncoming checks that every commit in from..to carries an SSH
// signature by one of signers (git's allowed-signers lines) or an OpenPGP
// signature by one of gpgKeys (armored public keys). Neither is checked
// against the machine's own keys: SSH against a file of signers, OpenPGP
// against a keyring holding only gpgKeys.
func verifyIncoming(dir string, signers, gpgKeys []string, from, to string) error {
	f, err := os.CreateTemp("", "nepfleet-allowed-signers-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(strings.Join(signers, "\n") + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	list, err := gitOut(dir, "rev-list", from+".."+to)
	if err != nil {
		return err
	}
	var home string // the OpenPGP keyring, made at the first such commit
	defer func() {
		if home != "" {
			removeKeyring(home)
		}
	}()
	for _, c := range strings.Fields(list) {
		short := c
		if len(short) > 12 {
			short = short[:12]
		}
		raw, err := gitOut(dir, "cat-file", "commit", c)
		if err != nil {
			return err
		}
		switch signatureKind(raw) {
		case "ssh":
			if len(signers) == 0 {
				return fmt.Errorf("%w: commit %s has an SSH signature, and sync.allowedSigners lists no key", errUnsigned, short)
			}
			// minTrustLevel fully: only a key listed in allowedSigners counts;
			// some git versions accept a valid signature by an unknown key with
			// trust "undefined".
			if _, err := gitOut(dir, "-c", "gpg.ssh.allowedSignersFile="+f.Name(), "-c", "gpg.minTrustLevel=fully", "verify-commit", c); err != nil {
				return fmt.Errorf("%w: commit %s isn't signed by a key in sync.allowedSigners", errUnsigned, short)
			}
		case "pgp":
			if len(gpgKeys) == 0 {
				return fmt.Errorf("%w: commit %s has an OpenPGP signature, and sync.allowedGPGKeys lists no key", errUnsigned, short)
			}
			if home == "" {
				if home, err = gpgKeyring(gpgKeys); err != nil {
					return err
				}
			}
			// gpg itself, whatever the repository's config names (the
			// format's own setting wins over gpg.program); only the keyring's
			// keys, which it trusts ultimately.
			cmd := exec.Command("git", "-c", "gpg.program=gpg", "-c", "gpg.openpgp.program=gpg", "-c", "gpg.minTrustLevel=fully", "verify-commit", c) //nolint:gosec // a commit id from rev-list
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
			if out, err := cmd.CombinedOutput(); err != nil {
				log.Printf("sync: commit %s: %s", short, strings.TrimSpace(string(out)))
				return fmt.Errorf("%w: commit %s isn't signed by a key in sync.allowedGPGKeys", errUnsigned, short)
			}
		default:
			return fmt.Errorf("%w: commit %s has no single SSH or OpenPGP signature", errUnsigned, short)
		}
	}
	return nil
}

// gpgKeyring makes a GnuPG home holding only keys, each trusted ultimately
// there, so that verify-commit counts exactly these keys.
func gpgKeyring(keys []string) (string, error) {
	home, err := os.MkdirTemp("", "nepfleet-gnupg-")
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		removeKeyring(home)
		return "", err
	}
	// GnuPG 2.4 sets a new home up for keyboxd, a daemon that would outlive
	// it, unless common.conf exists already.
	if err := os.WriteFile(filepath.Join(home, "common.conf"), nil, 0o600); err != nil {
		return fail(err)
	}
	gpg := func(stdin string, args ...string) (string, error) {
		cmd := exec.Command("gpg", append([]string{"--homedir", home, "--batch", "--no-tty"}, args...)...) //nolint:gosec // fixed arguments
		cmd.Stdin = strings.NewReader(stdin)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("gpg %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
		}
		return string(out), nil
	}
	if _, err := gpg(strings.Join(keys, "\n"), "--import"); err != nil {
		return fail(fmt.Errorf("sync.allowedGPGKeys: %w", err))
	}
	listing, err := gpg("", "--with-colons", "--list-keys")
	if err != nil {
		return fail(err)
	}
	// Each primary key's fingerprint is the fpr line right after its pub line.
	var trust strings.Builder
	primary := false
	for _, line := range strings.Split(listing, "\n") {
		field := strings.Split(line, ":")
		switch field[0] {
		case "pub":
			primary = true
		case "sub":
			primary = false
		case "fpr":
			if primary && len(field) > 9 && field[9] != "" {
				trust.WriteString(field[9] + ":6:\n") // 6: ultimate
			}
			primary = false
		}
	}
	if trust.Len() == 0 {
		return fail(errors.New("sync.allowedGPGKeys holds no OpenPGP public key"))
	}
	if _, err := gpg(trust.String(), "--import-ownertrust"); err != nil {
		return fail(err)
	}
	return home, nil
}

// removeKeyring stops what GnuPG may have started for home, and removes it.
func removeKeyring(home string) {
	_ = exec.Command("gpgconf", "--homedir", home, "--kill", "all").Run() //nolint:gosec // our own temporary directory
	_ = os.RemoveAll(home)
}

// signatureKind is the kind of a raw commit's signature header: "ssh",
// "pgp", or "" (none, another kind such as X.509, or more than one). Git
// verifies only one of gpgsig and gpgsig-sha256, by the repository's
// format, so a commit with both could be checked as one kind and verified
// as the other: it counts as unsigned.
func signatureKind(commit string) string {
	head, _, _ := strings.Cut(commit, "\n\n") // headers end at the first blank line
	kind, found := "", 0
	for _, line := range strings.Split(head, "\n") {
		v, ok := strings.CutPrefix(line, "gpgsig ")
		if !ok {
			v, ok = strings.CutPrefix(line, "gpgsig-sha256 ")
		}
		if !ok {
			continue
		}
		found++
		switch strings.TrimSpace(v) {
		case "-----BEGIN SSH SIGNATURE-----":
			kind = "ssh"
		case "-----BEGIN PGP SIGNATURE-----":
			kind = "pgp"
		default:
			kind = ""
		}
	}
	if found != 1 {
		return ""
	}
	return kind
}

var errUnsigned = errors.New("sync stopped: fleet.yaml requires signed commits")
