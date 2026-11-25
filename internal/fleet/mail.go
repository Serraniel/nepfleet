// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Mail is the fleet's SMTP server (fleet.yaml spec.mail). Stacks get it as
// ${NEPFLEET_SMTP_HOST}, _PORT, _USER, _PASSWORD, _FROM and _TLS.
type Mail struct {
	// Host is the SMTP server's host name or IP address.
	Host string `yaml:"host" schema:"required"`
	// Port defaults to 587 for starttls, 465 for tls and 25 for none.
	Port int `yaml:"port"`
	// User logs in to the server; empty sends without logging in.
	User string `yaml:"user"`
	// PasswordSecret names the key in fleet.secret.yaml holding the password.
	PasswordSecret string `yaml:"passwordSecret"`
	// From is the sender address, e.g. "NepFleet <fleet@example.org>".
	From string `yaml:"from"`
	// TLS: starttls upgrades a plain connection (port 587), tls connects
	// with TLS from the start (port 465), none sends in plain text (a relay
	// on the same network).
	TLS string `yaml:"tls" schema:"enum=starttls|tls|none;default=starttls"`
}

// MailTLS are the values of spec.mail.tls.
var MailTLS = []string{"starttls", "tls", "none"}

// MailPasswordKey is the fleet secret the app stores the mail password in.
const MailPasswordKey = "MAIL_PASSWORD"

// DefaultMailPort is the usual port for a tls mode.
func DefaultMailPort(tls string) int {
	switch tls {
	case "tls":
		return 465
	case "none":
		return 25
	}
	return 587
}

var secretNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// CheckMail validates mail settings; field names are relative to spec.mail.
func CheckMail(m Mail) error {
	switch {
	case !storageAddrRe.MatchString(m.Host):
		return fmt.Errorf("host %q is not a host name or IP address", m.Host)
	case m.Port < 0 || m.Port > 65535:
		return fmt.Errorf("port %d is not a port number", m.Port)
	case m.TLS != "" && m.TLS != "starttls" && m.TLS != "tls" && m.TLS != "none":
		return errors.New("tls is starttls, tls or none")
	case strings.ContainsFunc(m.User, isControl):
		return errors.New("user must not contain control characters")
	case m.PasswordSecret != "" && !secretNameRe.MatchString(m.PasswordSecret):
		return fmt.Errorf("passwordSecret %q is not a key name", m.PasswordSecret)
	case m.PasswordSecret != "" && m.User == "":
		return errors.New("a password needs a user")
	}
	if m.From != "" {
		// It goes into mail headers: one address, nothing that ends a line.
		if strings.ContainsFunc(m.From, isControl) {
			return errors.New("from must not contain control characters")
		}
		if _, err := mail.ParseAddress(m.From); err != nil {
			return fmt.Errorf("from %q is not a mail address", m.From)
		}
	}
	return nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// checkMail validates spec.mail and fills in its defaults.
func checkMail(f *Fleet) error {
	m := f.Spec.Mail
	if m == nil {
		return nil
	}
	if err := CheckMail(*m); err != nil {
		return fmt.Errorf("fleet.yaml: spec.mail: %w", err)
	}
	if m.TLS == "" {
		m.TLS = "starttls"
	}
	if m.Port == 0 {
		m.Port = DefaultMailPort(m.TLS)
	}
	return nil
}

// SetMail writes m as fleet.yaml's spec.mail, keeping the rest of the file
// and its comments. Empty fields are left out.
func SetMail(dir string, m Mail) error {
	if err := CheckMail(m); err != nil {
		return err
	}
	return editFleet(dir, func(spec *yaml.Node) error {
		n := &yaml.Node{Kind: yaml.MappingNode}
		put := func(k, v string) {
			if v != "" {
				mapPut(n, k, &yaml.Node{Kind: yaml.ScalarNode, Value: v})
			}
		}
		put("host", m.Host)
		if m.Port != 0 {
			mapPut(n, "port", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(m.Port)})
		}
		put("user", m.User)
		put("passwordSecret", m.PasswordSecret)
		put("from", m.From)
		put("tls", m.TLS)
		mapPut(spec, "mail", n)
		return nil
	}, func(f *Fleet) error {
		if f.Spec.Mail == nil || f.Spec.Mail.Host != m.Host {
			return errors.New("the mail settings aren't there after writing them")
		}
		return nil
	})
}

// RemoveMail deletes spec.mail; it reports whether there was one.
func RemoveMail(dir string) (bool, error) {
	found := false
	err := editFleet(dir, func(spec *yaml.Node) error {
		found = mapDelete(spec, "mail")
		return nil
	}, func(f *Fleet) error {
		if f.Spec.Mail != nil {
			return errors.New("the mail settings are still there after removing them")
		}
		return nil
	})
	return found, err
}
