// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Serraniel/nepfleet/internal/doctor"
)

// keyBackup records that a copy of the age key was made (in the state
// directory, not the fleet repository: it describes this host's key).
type keyBackup struct {
	Created   time.Time `json:"created"`
	File      string    `json:"file"`
	Method    string    `json:"method"` // passphrase | recipients
	PublicKey string    `json:"publicKey"`
}

// mirrorState records when this host last mirrored the repository.
type mirrorState map[string]time.Time

const (
	keyBackupFile = "key-backup.json"
	mirrorFile    = "mirror.json"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// cmdKey: nepfleet key backup --out FILE [--recipient age1…]
func cmdKey(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "backup" {
		return errors.New("usage: nepfleet key backup --out FILE [--recipient age1…]")
	}
	var c common
	fs := flag.NewFlagSet("key backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	out := fs.String("out", "", "file to write the encrypted key to; keep it off this host (a USB stick, a password manager, another machine)")
	var recipients stringList
	fs.Var(&recipients, "recipient", "encrypt to this age public key (repeatable) instead of a passphrase")
	if _, err := parseInterspersed(fs, args[1:]); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("--out is required")
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	key := ageKeyFile()
	if _, err := os.Stat(key); err != nil {
		for _, k := range []string{"SOPS_AGE_KEY", "SOPS_AGE_KEY_CMD"} {
			if os.Getenv(k) != "" {
				return fmt.Errorf("the key comes from %s, not a file: back up where %s gets it from", k, k)
			}
		}
		return fmt.Errorf("no age key at %s", key)
	}
	abs, err := backupTarget(e.fleet.Dir, *out)
	if err != nil {
		return err
	}
	pub, err := exec.Command("age-keygen", "-y", "--", key).Output()
	if err != nil {
		return fmt.Errorf("reading %s: %w", key, cmdErr(err))
	}
	// Created here, owner-only and exclusively (no file, no symlink may be
	// in its place); age writes to it as its output.
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ageArgs := []string{"--armor"}
	method := "passphrase"
	if len(recipients) > 0 {
		method = "recipients"
		for _, r := range recipients {
			ageArgs = append(ageArgs, "--recipient", r)
		}
	} else {
		ageArgs = append(ageArgs, "--passphrase") // age asks on the terminal
	}
	cmd := exec.Command("age", append(ageArgs, "--", key)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, f, stderr
	err = cmd.Run()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(abs) // the file this command created
		return fmt.Errorf("age: %w", err)
	}
	rec, err := json.MarshalIndent(keyBackup{Created: time.Now().UTC(), File: abs, Method: method, PublicKey: strings.TrimSpace(string(pub))}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeState(c.stateDir, keyBackupFile, rec); err != nil {
		return err
	}
	restore := fmt.Sprintf("age -d -o %s -- %s", shellQuote(key), shellQuote(abs))
	if method == "recipients" {
		restore = fmt.Sprintf("age -d -i <identity of a recipient> -o %s -- %s", shellQuote(key), shellQuote(abs))
	}
	fmt.Fprintf(stdout, "wrote %s (%s, owner-only); move it off this host.\nrestore with: %s\n", abs, method, restore)
	return nil
}

// backupTarget is the absolute output path, refused when it is (or, through
// symlinks, ends up) inside the git repository of the fleet, or exists.
func backupTarget(fleetDir, out string) (string, error) {
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(abs); err == nil {
		return "", fmt.Errorf("%s already exists", abs)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("the folder for %s: %w", abs, err)
	}
	repo := fleetDir
	if top, err := gitOut(fleetDir, "rev-parse", "--show-toplevel"); err == nil {
		repo = strings.TrimSpace(top) // --fleet may be a subfolder of the repository
	}
	if repo, err = filepath.EvalSymlinks(repo); err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(repo, parent); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("the copy must not go into the fleet repository: whoever has the repository would only need the passphrase")
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

// cmdMirror pushes the fleet repository's branches and tags to fleet.yaml's
// spec.mirrors. It pushes what origin has (fetched first), never forces and
// never deletes: a mirror that has other commits makes it fail instead.
func cmdMirror(args []string, stdout, stderr io.Writer) error {
	var c common
	fs := flag.NewFlagSet("mirror", flag.ContinueOnError)
	fs.SetOutput(stderr)
	c.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	mirrors := e.fleet.Spec.Mirrors
	if len(mirrors) == 0 {
		return errors.New("fleet.yaml has no spec.mirrors (git URLs of second remotes)")
	}
	dir := e.fleet.Dir
	refspecs := []string{"refs/tags/*:refs/tags/*"}
	if _, err := gitOut(dir, "remote", "get-url", "origin"); err == nil {
		// origin is the truth: a clone that is behind must not publish its
		// older branches.
		if _, err := gitOut(dir, "fetch", "--quiet", "origin"); err != nil {
			return err
		}
		refs, err := gitOut(dir, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/")
		if err != nil {
			return err
		}
		for _, r := range strings.Fields(refs) {
			if r == "refs/remotes/origin/HEAD" {
				continue
			}
			refspecs = append(refspecs, r+":refs/heads/"+strings.TrimPrefix(r, "refs/remotes/origin/"))
		}
	} else {
		refspecs = append(refspecs, "refs/heads/*:refs/heads/*")
	}
	state := mirrorState{}
	if raw, err := os.ReadFile(filepath.Join(c.stateDir, mirrorFile)); err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	var errs []error
	for _, m := range mirrors {
		if _, err := gitOut(dir, append([]string{"push", "--quiet", "--", m}, refspecs...)...); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w (a mirror that has commits of its own is not overwritten)", m, err))
			continue
		}
		state[m] = time.Now().UTC()
		fmt.Fprintf(stdout, "mirrored to %s\n", m)
	}
	if raw, err := json.MarshalIndent(state, "", "  "); err == nil {
		if err := writeState(c.stateDir, mirrorFile, raw); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func writeState(dir, name string, raw []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), raw, 0o600)
}

// backupChecks report whether the age key and the fleet repository exist
// somewhere else: losing the key loses every secret.
func backupChecks(e *env, stateDir, keyFromEnv string) []doctor.Check {
	var out []doctor.Check
	rec := readKeyBackup(stateDir)
	_, copyErr := os.Stat(func() string {
		if rec != nil {
			return rec.File
		}
		return ""
	}())
	switch {
	case len(e.fleet.Spec.Secrets.Age) > 1:
		out = append(out, doctor.Check{Name: "Key copy", Status: doctor.OK, Detail: fmt.Sprintf("%d recipients in fleet.yaml: once the files are re-encrypted (sops updatekeys), each key decrypts them; keep them on different machines", len(e.fleet.Spec.Secrets.Age))})
	case keyFromEnv != "":
		out = append(out, doctor.Check{Name: "Key copy", Status: doctor.Warn, Detail: "the key comes from " + keyFromEnv + "; whether a copy exists cannot be checked", Fix: "keep a copy of its source off this host"})
	case rec != nil && keyMatches(rec.PublicKey) && copyErr != nil:
		out = append(out, doctor.Check{Name: "Key copy", Status: doctor.Warn, Detail: rec.File + " is no longer there (moved off this host?)", Fix: "fine if you moved it; otherwise nepfleet key backup again"})
	case rec != nil && keyMatches(rec.PublicKey):
		out = append(out, doctor.Check{Name: "Key copy", Status: doctor.OK, Detail: fmt.Sprintf("copied %s to %s (%s)", rec.Created.Format("2006-01-02"), rec.File, rec.Method)})
	default:
		out = append(out, doctor.Check{Name: "Key copy", Status: doctor.Warn, Detail: "no copy of the age key is known: losing it loses every secret",
			Fix: "nepfleet key backup --out /media/usb/nepfleet-key.age (or add a second recipient to fleet.yaml)"})
	}
	state := mirrorState{}
	if raw, err := os.ReadFile(filepath.Join(stateDir, mirrorFile)); err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	var never []string
	for _, m := range e.fleet.Spec.Mirrors {
		if state[m].IsZero() {
			never = append(never, m)
		}
	}
	switch {
	case len(e.fleet.Spec.Mirrors) == 0:
		out = append(out, doctor.Check{Name: "Repository mirror", Status: doctor.Warn, Detail: "the fleet repository has no second remote", Fix: "add spec.mirrors to fleet.yaml, then run nepfleet mirror"})
	case len(never) > 0:
		out = append(out, doctor.Check{Name: "Repository mirror", Status: doctor.Warn, Detail: "not mirrored from this host yet: " + strings.Join(never, ", "), Fix: "nepfleet mirror (from a timer, or after pushing)"})
	default:
		out = append(out, doctor.Check{Name: "Repository mirror", Status: doctor.OK, Detail: strings.Join(e.fleet.Spec.Mirrors, ", ")})
	}
	return out
}

func readKeyBackup(stateDir string) *keyBackup {
	raw, err := os.ReadFile(filepath.Join(stateDir, keyBackupFile))
	if err != nil {
		return nil
	}
	var rec keyBackup
	if json.Unmarshal(raw, &rec) != nil {
		return nil
	}
	return &rec
}

// keyMatches: the recorded copy is of the key in use now.
func keyMatches(pub string) bool {
	out, err := exec.Command("age-keygen", "-y", "--", ageKeyFile()).Output()
	return err == nil && strings.TrimSpace(string(out)) == pub
}

// cmdErr adds a command's stderr to its error.
func cmdErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
