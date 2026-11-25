// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

func TestMailAPI(t *testing.T) {
	c, dir := secretFleet(t)
	pw := "s3cret-pw"
	if _, err := setMailAPI(c, api.MailUpdate{Host: "smtp.example.test", User: "u@example.test", Password: &pw}, "ci"); err != nil {
		t.Fatal(err)
	}
	m, err := mailAPI(c)
	if err != nil || !m.Configured || !m.PasswordSet || m.Port != 587 {
		t.Fatalf("after setting: %+v %v", m, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, fleet.FleetSecretsFile))
	if strings.Contains(string(raw), pw) {
		t.Fatal("the password is in plain text")
	}

	// A helo set by hand stays when the app saves.
	f, _ := os.ReadFile(filepath.Join(dir, "fleet.yaml"))
	f = []byte(strings.Replace(string(f), "host: smtp.example.test", "host: smtp.example.test\n    helo: fleet.example.test", 1))
	if err := os.WriteFile(filepath.Join(dir, "fleet.yaml"), f, 0o600); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "commit", "-qam", "helo")
	if _, err := setMailAPI(c, api.MailUpdate{Host: "smtp.example.test", User: "u@example.test"}, "ci"); err != nil {
		t.Fatal(err)
	}
	e, _ := c.open()
	if mm := e.fleet.Spec.Mail; mm.Helo != "fleet.example.test" || mm.PasswordSecret != fleet.MailPasswordKey {
		t.Errorf("kept: %+v", mm)
	}

	// Clearing the user clears the password with it, rather than failing.
	if _, err := setMailAPI(c, api.MailUpdate{Host: "relay", TLS: "none"}, "ci"); err != nil {
		t.Fatalf("clearing the user: %v", err)
	}
	if m, _ := mailAPI(c); m.User != "" || m.PasswordSet {
		t.Errorf("after clearing the user: %+v", m)
	}
	e, _ = c.open()
	if secrets, _ := e.store.Load(filepath.Join(dir, fleet.FleetSecretsFile)); secrets[fleet.MailPasswordKey] != "" {
		t.Error("the password stayed in the secrets")
	}
	// A new password without a user is still refused.
	if _, err := setMailAPI(c, api.MailUpdate{Host: "relay", Password: &pw}, "ci"); !errors.As(err, new(api.InvalidError)) {
		t.Errorf("a password without a user: %v", err)
	}

	// Settings that can't send are a settings error, not a failed send.
	if err := testMailAPI(context.Background(), c, "a@example.test"); !errors.As(err, new(api.InvalidError)) {
		t.Errorf("no sender: %v", err)
	}
	if st := tgit(t, dir, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("left uncommitted: %s", st)
	}
}
