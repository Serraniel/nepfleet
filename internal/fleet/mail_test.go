// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMailValidation(t *testing.T) {
	for raw, ok := range map[string]bool{
		`{host: smtp.example.test}`: true,
		`{host: smtp.example.test, port: 465, tls: tls, user: u@example.test, passwordSecret: MAIL_PASSWORD, from: "Fleet <fleet@example.test>"}`: true,
		`{host: relay, tls: none}`:                           true,
		`{host: "smtp example"}`:                             false,
		`{port: 25}`:                                         false,
		`{host: a, port: 70000}`:                             false,
		`{host: a, tls: ssl}`:                                false,
		`{host: a, from: "not an address"}`:                  false,
		`{host: a, from: "a@example.test\nBcc: b@x.test"}`:   false,
		`{host: a, passwordSecret: P}`:                       false,
		`{host: a, user: u, passwordSecret: "bad key"}`:      false,
		`{host: a, user: "u\r\n"}`:                           false,
		`{host: a, user: u, passwordSecret: P, password: x}`: false,
	} {
		dir := t.TempDir()
		y := strings.Replace(skeleton["fleet.yaml"], "spec:\n", "spec:\n  mail: "+raw+"\n", 1)
		if err := os.WriteFile(filepath.Join(dir, "fleet.yaml"), []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); (err == nil) != ok {
			t.Errorf("%s: err = %v", raw, err)
		}
	}
}

func TestMailDefaults(t *testing.T) {
	for tls, port := range map[string]int{"": 587, "starttls": 587, "tls": 465, "none": 25} {
		dir := t.TempDir()
		m := "{host: smtp.example.test}"
		if tls != "" {
			m = "{host: smtp.example.test, tls: " + tls + "}"
		}
		y := strings.Replace(skeleton["fleet.yaml"], "spec:\n", "spec:\n  mail: "+m+"\n", 1)
		if err := os.WriteFile(filepath.Join(dir, "fleet.yaml"), []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if f.Spec.Mail.Port != port || f.Spec.Mail.TLS == "" {
			t.Errorf("tls %q: %+v", tls, f.Spec.Mail)
		}
	}
}

func TestSetMail(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	want := Mail{Host: "smtp.example.test", Port: 2525, User: "fleet@example.test", PasswordSecret: MailPasswordKey, From: "Fleet <fleet@example.test>", TLS: "tls"}
	if err := SetMail(dir, want); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if *f.Spec.Mail != want {
		t.Errorf("got %+v", *f.Spec.Mail)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "fleet.yaml"))
	if !strings.Contains(string(raw), "# Secrets are committed encrypted") {
		t.Error("the comments went")
	}
	if err := SetMail(dir, Mail{Host: "bad host"}); err == nil {
		t.Error("an invalid host was written")
	}
	// Replaced whole: fields left out are gone.
	if err := SetMail(dir, Mail{Host: "relay", TLS: "none"}); err != nil {
		t.Fatal(err)
	}
	f, _ = Load(dir)
	if m := f.Spec.Mail; m.User != "" || m.PasswordSecret != "" || m.Port != 25 {
		t.Errorf("replaced: %+v", m)
	}
	if found, err := RemoveMail(dir); err != nil || !found {
		t.Fatalf("remove: %v %v", found, err)
	}
	f, _ = Load(dir)
	if f.Spec.Mail != nil {
		t.Error("still there")
	}
	if found, err := RemoveMail(dir); err != nil || found {
		t.Errorf("remove again: %v %v", found, err)
	}
}
