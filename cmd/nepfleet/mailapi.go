// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/mailer"
)

// mailAPI is GET /api/v1/mail: fleet.yaml's spec.mail and whether its
// password is stored, never the password.
func mailAPI(c common) (api.MailSettings, error) {
	e, err := c.open()
	if err != nil {
		return api.MailSettings{}, err
	}
	m := e.fleet.Spec.Mail
	if m == nil {
		return api.MailSettings{}, nil
	}
	out := api.MailSettings{Configured: true, Host: m.Host, Port: m.Port, User: m.User, From: m.From, TLS: m.TLS}
	if m.PasswordSecret != "" {
		keys, err := secretKeys(c, "")
		if err != nil {
			return api.MailSettings{}, err
		}
		out.PasswordSet = slices.Contains(keys, m.PasswordSecret)
	}
	return out, nil
}

// setMailAPI is PUT /api/v1/mail: spec.mail in fleet.yaml and the password
// in fleet.secret.yaml, committed together. A password key set by hand
// (passwordSecret) is kept and written to.
func setMailAPI(c common, u api.MailUpdate, by string) (string, error) {
	m := fleet.Mail{Host: u.Host, Port: u.Port, User: u.User, From: u.From, TLS: u.TLS}
	return changeChannels(c, by, "chore(fleet): set mail settings", func(e *env, secrets map[string]string) (bool, error) {
		key := fleet.MailPasswordKey
		if old := e.fleet.Spec.Mail; old != nil && old.PasswordSecret != "" {
			key = old.PasswordSecret
		}
		changed := false
		switch {
		case u.Password == nil:
			if _, ok := secrets[key]; ok {
				m.PasswordSecret = key
			}
		case *u.Password == "":
			if _, ok := secrets[key]; ok {
				delete(secrets, key)
				changed = true
			}
		default:
			secrets[key] = *u.Password
			m.PasswordSecret = key
			changed = true
		}
		if err := fleet.SetMail(e.fleet.Dir, m); err != nil {
			return false, api.InvalidError{Reason: cleanReason(err, e.fleet.Dir)}
		}
		return changed, nil
	})
}

// removeMailAPI is DELETE /api/v1/mail: spec.mail and the password the app
// stored (one set by hand under another key may be shared, so it stays).
func removeMailAPI(c common, by string) (string, error) {
	return changeChannels(c, by, "chore(fleet): remove mail settings", func(e *env, secrets map[string]string) (bool, error) {
		m := e.fleet.Spec.Mail
		if m == nil {
			return false, api.ErrNoMail
		}
		if _, err := fleet.RemoveMail(e.fleet.Dir); err != nil {
			return false, err
		}
		if m.PasswordSecret == fleet.MailPasswordKey {
			if _, ok := secrets[m.PasswordSecret]; ok {
				delete(secrets, m.PasswordSecret)
				return true, nil
			}
		}
		return false, nil
	})
}

// testMailAPI is POST /api/v1/mail/test: a test mail to to with the saved
// settings.
func testMailAPI(ctx context.Context, c common, to string) error {
	e, err := c.open()
	if err != nil {
		return err
	}
	m := e.fleet.Spec.Mail
	if m == nil {
		return api.ErrNoMail
	}
	password := ""
	if m.PasswordSecret != "" {
		sec, err := e.store.Load(filepath.Join(e.fleet.Dir, fleet.FleetSecretsFile))
		if err != nil {
			return err
		}
		var ok bool
		if password, ok = sec[m.PasswordSecret]; !ok {
			return fmt.Errorf("the password (%s in %s) is not set", m.PasswordSecret, fleet.FleetSecretsFile)
		}
	}
	return mailer.Send(ctx, *m, password, mailer.Message{
		To:      to,
		Subject: "NepFleet test mail",
		Body: "This is a test mail from NepFleet, fleet " + e.fleet.Metadata.Name + ".\n\n" +
			"Stacks that use the fleet's mail settings send through the same server.\n",
	})
}
