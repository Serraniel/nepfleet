// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nicholas-fedor/shoutrrr"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

// notSecretKey turns a channel name into its fleet secret's key.
var notSecretKey = regexp.MustCompile(`[^A-Z0-9_]`)

func channelSecretKey(name string) string {
	return "NOTIFY_" + notSecretKey.ReplaceAllString(strings.ToUpper(name), "_")
}

// setChannelAPI adds or replaces a notification channel from the app: the
// URL encrypted in fleet.secret.yaml, the channel (and a route to it) in
// fleet.yaml, committed together.
func setChannelAPI(c common, name, url, minSeverity, by string) (string, error) {
	// Only the URL's shape: nothing is sent (the app tests separately).
	if _, err := shoutrrr.CreateSenderWithOptions(types.SenderOptions{}, url); err != nil {
		return "", api.InvalidError{Reason: "not a notification URL shoutrrr knows: " + err.Error()}
	}
	key := channelSecretKey(name)
	return changeChannels(c, by, "chore(fleet): set notification channel "+name, func(e *env, secrets map[string]string) (bool, error) {
		secrets[key] = url
		return true, fleet.SetChannel(e.fleet.Dir, name, key, minSeverity)
	})
}

// removeChannelAPI deletes a channel, its routes' mentions of it and the
// secret the app stored for it.
func removeChannelAPI(c common, name, by string) (string, error) {
	return changeChannels(c, by, "chore(fleet): remove notification channel "+name, func(e *env, secrets map[string]string) (bool, error) {
		ch, ok := e.fleet.Spec.Notifications.Channels[name]
		if !ok {
			return false, api.ErrNoChannel
		}
		if _, err := fleet.RemoveChannel(e.fleet.Dir, name); err != nil {
			return false, err
		}
		// Only the secret the app made for it: one set by hand may be shared.
		if ch.URLSecret == channelSecretKey(name) {
			delete(secrets, ch.URLSecret)
			return true, nil
		}
		return false, nil
	})
}

// changeChannels runs change on fleet.yaml and the fleet's secrets, and
// commits both; on any failure both files are as they were. change reports
// whether the secrets changed.
func changeChannels(c common, by, msg string, change func(e *env, secrets map[string]string) (bool, error)) (string, error) {
	if !apiMu.TryLock() {
		return "", api.ErrBusy
	}
	defer apiMu.Unlock()
	e, err := c.open()
	if err != nil {
		return "", err
	}
	fleetFile := filepath.Join(e.fleet.Dir, "fleet.yaml")
	secretFile := filepath.Join(e.fleet.Dir, fleet.FleetSecretsFile)
	rels := []string{relTo(e.fleet.Dir, fleetFile), relTo(e.fleet.Dir, secretFile)}
	st, err := gitOut(e.fleet.Dir, append([]string{"status", "--porcelain", "--"}, rels...)...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(st) != "" {
		return "", api.ErrUncommitted
	}
	secrets, err := e.store.Load(secretFile)
	if err != nil {
		return "", err
	}
	if secrets == nil {
		secrets = map[string]string{}
	}
	saved := map[string][]byte{}
	for _, p := range []string{fleetFile, secretFile} {
		if raw, err := os.ReadFile(p); err == nil { //nolint:gosec // files of the fleet repository
			saved[p] = raw
		}
	}
	restore := func() {
		for _, p := range []string{fleetFile, secretFile} {
			if raw, ok := saved[p]; ok {
				_ = os.WriteFile(p, raw, 0o600) //nolint:gosec // as it was
			} else {
				_ = os.Remove(p)
			}
		}
	}
	secretsChanged, err := change(e, secrets)
	if err != nil {
		restore()
		return "", err
	}
	paths := rels[:1]
	if secretsChanged {
		if err := e.store.Save(secretFile, secrets); err != nil {
			restore()
			return "", err
		}
		paths = rels
	}
	if _, err := gitOut(e.fleet.Dir, append([]string{"add", "--"}, paths...)...); err != nil {
		restore()
		return "", err
	}
	if err := commitAs(e.fleet.Dir, apiAuthor(by), msg, paths...); err != nil {
		_, _ = gitOut(e.fleet.Dir, append([]string{"reset", "-q", "--"}, paths...)...)
		restore()
		return "", errors.Join(errors.New("committing the channel"), err)
	}
	head, err := gitOut(e.fleet.Dir, "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}
