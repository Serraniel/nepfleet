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
	"slices"
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

const keyBackupFile = "key-backup.json"

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
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("%s already exists", *out)
	}
	e, err := c.open()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	fleetDir, err := filepath.Abs(e.fleet.Dir)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(fleetDir, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("the copy must not go into the fleet repository: whoever has the repository would only need the passphrase")
	}
	key := ageKeyFile()
	pub, err := exec.Command("age-keygen", "-y", key).Output()
	if err != nil {
		return fmt.Errorf("reading %s: %w", key, err)
	}
	ageArgs := []string{"--armor", "--output", abs}
	method := "passphrase"
	if len(recipients) > 0 {
		method = "recipients"
		for _, r := range recipients {
			ageArgs = append(ageArgs, "--recipient", r)
		}
	} else {
		ageArgs = append(ageArgs, "--passphrase") // age asks on the terminal
	}
	cmd := exec.Command("age", append(ageArgs, key)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(abs)
		return fmt.Errorf("age: %w", err)
	}
	rec, err := json.MarshalIndent(keyBackup{Created: time.Now().UTC(), File: abs, Method: method, PublicKey: strings.TrimSpace(string(pub))}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.stateDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(c.stateDir, keyBackupFile), rec, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s (%s); move it off this host. Restore with: age -d %s > %s\n", abs, method, filepath.Base(abs), key)
	return nil
}

// cmdMirror pushes the fleet repository to fleet.yaml's spec.mirrors and
// adds them as push URLs of origin, so a plain git push mirrors too.
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
	if origin, err := gitOut(dir, "remote", "get-url", "origin"); err == nil {
		push := pushURLs(dir)
		// The first push URL replaces the fetch URL for pushes: keep origin.
		if len(push) == 0 {
			if _, err := gitOut(dir, "remote", "set-url", "--add", "--push", "origin", strings.TrimSpace(origin)); err != nil {
				return err
			}
		}
		for _, m := range mirrors {
			if !slices.Contains(push, m) {
				if _, err := gitOut(dir, "remote", "set-url", "--add", "--push", "origin", m); err != nil {
					return err
				}
				fmt.Fprintf(stdout, "git push now also pushes to %s\n", m)
			}
		}
	}
	var errs []error
	for _, m := range mirrors {
		// Every branch and tag, also rewritten ones; branches deleted here
		// are deleted there: it is a mirror.
		if _, err := gitOut(dir, "push", "--prune", m, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", m, err))
			continue
		}
		fmt.Fprintf(stdout, "mirrored to %s\n", m)
	}
	return errors.Join(errs...)
}

func pushURLs(dir string) []string {
	out, err := gitOut(dir, "config", "--get-all", "remote.origin.pushurl")
	if err != nil {
		return nil
	}
	return strings.Fields(out)
}

// backupChecks report whether the age key and the fleet repository exist
// somewhere else: losing the key loses every secret.
func backupChecks(e *env, stateDir string) []doctor.Check {
	var out []doctor.Check
	switch rec := readKeyBackup(stateDir); {
	case len(e.fleet.Spec.Secrets.Age) > 1:
		out = append(out, doctor.Check{Name: "Key copy", Status: doctor.OK, Detail: fmt.Sprintf("%d age recipients can decrypt the secrets", len(e.fleet.Spec.Secrets.Age))})
	case rec != nil && keyMatches(rec.PublicKey):
		out = append(out, doctor.Check{Name: "Key copy", Status: doctor.OK, Detail: fmt.Sprintf("copied %s to %s (%s)", rec.Created.Format("2006-01-02"), rec.File, rec.Method)})
	default:
		out = append(out, doctor.Check{Name: "Key copy", Status: doctor.Warn, Detail: "no copy of the age key is known: losing it loses every secret",
			Fix: "nepfleet key backup --out /media/usb/nepfleet-key.age (or add a second recipient to fleet.yaml)"})
	}
	switch push := pushURLs(e.fleet.Dir); {
	case len(e.fleet.Spec.Mirrors) == 0:
		out = append(out, doctor.Check{Name: "Repository mirror", Status: doctor.Warn, Detail: "the fleet repository has no second remote", Fix: "add spec.mirrors to fleet.yaml, then run nepfleet mirror"})
	case !containsAll(push, e.fleet.Spec.Mirrors):
		out = append(out, doctor.Check{Name: "Repository mirror", Status: doctor.Warn, Detail: "git push does not reach every mirror from this clone", Fix: "nepfleet mirror"})
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
	out, err := exec.Command("age-keygen", "-y", ageKeyFile()).Output()
	return err == nil && strings.TrimSpace(string(out)) == pub
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}
