// SPDX-License-Identifier: AGPL-3.0-or-later

// Package secrets reads and writes SOPS-encrypted value files (age keys).
// Plaintext exists only in memory and in the rendered env file on the target
// host, never in the fleet repository.
package secrets

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Store loads and saves flat string maps.
type Store interface {
	Load(path string) (map[string]string, error)
	Save(path string, values map[string]string) error
}

// SOPS shells out to the sops binary. Decryption uses the usual sops key
// lookup (SOPS_AGE_KEY_FILE, ~/.config/sops/age/keys.txt).
type SOPS struct {
	// Binary defaults to "sops" on PATH.
	Binary string
	// Recipients are the age public keys files are encrypted to.
	Recipients []string
}

func (s SOPS) bin() string {
	if s.Binary != "" {
		return s.Binary
	}
	return "sops"
}

// Load decrypts path. A missing file is an empty map.
func (s SOPS) Load(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	} else if err != nil {
		return nil, err
	}
	if !IsEncrypted(raw) {
		return nil, fmt.Errorf("%s is not SOPS-encrypted; refusing to use plaintext secrets", path)
	}
	cmd := exec.Command(s.bin(), "decrypt", "--input-type", "yaml", "--output-type", "json", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sops decrypt %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, err
	}
	values := map[string]string{}
	for k, v := range m {
		if k == "sops" {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s: %s must be a string", path, k)
		}
		values[k] = s
	}
	return values, nil
}

// Save encrypts values to path atomically.
func (s SOPS) Save(path string, values map[string]string) error {
	if len(s.Recipients) == 0 {
		return errors.New("no age recipients: add your public key to fleet.yaml spec.secrets.age")
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	doc := yaml.Node{Kind: yaml.MappingNode}
	for _, k := range keys {
		doc.Content = append(doc.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: k},
			&yaml.Node{Kind: yaml.ScalarNode, Value: values[k], Style: yaml.DoubleQuotedStyle})
	}
	plain, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	cmd := exec.Command(s.bin(), "encrypt", "--age", strings.Join(s.Recipients, ","),
		"--input-type", "yaml", "--output-type", "yaml", "--filename-override", path, "/dev/stdin")
	cmd.Stdin = bytes.NewReader(plain)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("sops encrypt: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if !IsEncrypted(out) {
		return errors.New("sops produced no encrypted output")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secret-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// IsEncrypted reports whether raw is a SOPS document in which every value
// (outside the sops metadata) is encrypted. A plaintext key added by hand
// to an encrypted file makes it fail.
func IsEncrypted(raw []byte) bool {
	var doc map[string]any
	if yaml.Unmarshal(raw, &doc) != nil {
		return false
	}
	meta, ok := doc["sops"].(map[string]any)
	if !ok || len(meta) == 0 {
		return false
	}
	delete(doc, "sops")
	return allEncrypted(doc)
}

func allEncrypted(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for _, x := range t {
			if !allEncrypted(x) {
				return false
			}
		}
		return true
	case []any:
		for _, x := range t {
			if !allEncrypted(x) {
				return false
			}
		}
		return true
	case string:
		return strings.HasPrefix(t, "ENC[")
	case nil:
		return true
	}
	return false // numbers, booleans: SOPS would have encrypted them
}

// Memory is an in-memory Store for tests.
type Memory map[string]map[string]string

// Load returns a copy of the stored map.
func (m Memory) Load(path string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range m[path] {
		out[k] = v
	}
	return out, nil
}

// Save stores a copy.
func (m Memory) Save(path string, values map[string]string) error {
	c := map[string]string{}
	for k, v := range values {
		c[k] = v
	}
	m[path] = c
	return nil
}
