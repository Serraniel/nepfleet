// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

// secretPath is the encrypted file of a stack, or of the fleet for "".
func secretPath(e *env, stack string) (string, error) {
	if stack == "" {
		return filepath.Join(e.fleet.Dir, fleet.FleetSecretsFile), nil
	}
	s, err := e.fleet.LoadStack(stack)
	if err != nil {
		return "", api.ErrNotFound
	}
	return filepath.Join(s.Dir, fleet.SecretsFile), nil
}

// secretKeys reads the keys of an encrypted file without decrypting it: SOPS
// leaves the keys in plain text.
func secretKeys(c common, stack string) ([]string, error) {
	e, err := c.open()
	if err != nil {
		return nil, err
	}
	path, err := secretPath(e, stack)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path) //nolint:gosec // a path inside the fleet repository
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	} else if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		if k != "sops" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// setSecretAPI stores (or, with a nil value, removes) one secret, encrypted,
// and commits the file as by. The value never reaches a log or an error.
func setSecretAPI(c common, stack, key string, value *string, by string) (string, error) {
	if !apiMu.TryLock() {
		return "", api.ErrBusy
	}
	defer apiMu.Unlock()
	e, err := c.open()
	if err != nil {
		return "", err
	}
	path, err := secretPath(e, stack)
	if err != nil {
		return "", err
	}
	rel := relTo(e.fleet.Dir, path)
	st, err := gitOut(e.fleet.Dir, "status", "--porcelain", "--", rel)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(st) != "" {
		return "", api.ErrUncommitted
	}
	values, err := e.store.Load(path)
	if err != nil {
		return "", err
	}
	where := stack
	if where == "" {
		where = "fleet"
	}
	msg := "chore(" + where + "): set secret " + key
	if value == nil {
		if _, ok := values[key]; !ok {
			return "", api.ErrNoSecret
		}
		delete(values, key)
		msg = "chore(" + where + "): remove secret " + key
	} else {
		values[key] = *value
	}
	before, readErr := os.ReadFile(path) //nolint:gosec // a path inside the fleet repository
	restore := func() {
		if readErr != nil {
			_ = os.Remove(path)
			return
		}
		_ = os.WriteFile(path, before, 0o600) //nolint:gosec // the same path, as it was
	}
	if err := e.store.Save(path, values); err != nil {
		return "", err
	}
	if _, err := gitOut(e.fleet.Dir, "add", "--", rel); err != nil {
		restore()
		return "", err
	}
	if err := commitAs(e.fleet.Dir, apiAuthor(by), msg, rel); err != nil {
		_, _ = gitOut(e.fleet.Dir, "reset", "-q", "--", rel)
		restore()
		return "", err
	}
	head, err := gitOut(e.fleet.Dir, "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}
