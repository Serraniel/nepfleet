// SPDX-License-Identifier: AGPL-3.0-or-later

// Package auth holds API tokens for automation (scripts, CI, the UI during
// development). People sign in with passkeys or OIDC; tokens are for
// programs. Only a keyed hash of each token is stored, with a key of this
// installation, so the token file alone does not allow testing guesses.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Serraniel/nepfleet/internal/flock"
)

// Scopes, each including the ones before it.
const (
	ScopeRead  = "read"  // read stacks, plans and history
	ScopeApply = "apply" // also apply and roll back
	ScopeEdit  = "edit"  // also change stack files, committed: what runs, not only re-apply it
	ScopeAdmin = "admin" // also manage tokens and settings
)

// ScopeNotify only sends alerts to the fleet's notification channels, for tools
// such as Dozzle that hold the token in their config. It reads nothing, and
// only it and admin may send alerts.
const ScopeNotify = "notify"

var scopeRank = map[string]int{ScopeNotify: -1, ScopeRead: 0, ScopeApply: 1, ScopeEdit: 2, ScopeAdmin: 3}

// ValidScope reports a known scope.
func ValidScope(s string) bool { _, ok := scopeRank[s]; return ok }

// Prefix marks NepFleet tokens, so secret scanners can find leaked ones.
const Prefix = "nft_"

// Token is a stored token, without the secret.
type Token struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Hash    string    `json:"hash"`
	Scope   string    `json:"scope"`
	Stacks  []string  `json:"stacks,omitempty"` // empty: all stacks
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
}

// Allows reports whether the token may do something needing scope on stack
// ("" for requests not about one stack).
func (t Token) Allows(scope, stack string) bool {
	if scope == ScopeNotify {
		return t.Scope == ScopeNotify || t.Scope == ScopeAdmin
	}
	if r, ok := scopeRank[t.Scope]; !ok || r < scopeRank[scope] {
		return false
	}
	return stack == "" || len(t.Stacks) == 0 || slices.Contains(t.Stacks, stack)
}

// Store keeps tokens in <dir>/tokens.json and the hashing key in
// <dir>/token.key, both owner-only.
type Store struct {
	Dir string
	Now func() time.Time

	mu sync.Mutex
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Create makes a token and returns its secret, shown once.
func (s *Store) Create(name, scope string, stacks []string, ttl time.Duration) (secret string, t Token, err error) {
	if !ValidScope(scope) {
		return "", t, fmt.Errorf("scope is %s, %s, %s, %s or %s", ScopeRead, ScopeApply, ScopeEdit, ScopeAdmin, ScopeNotify)
	}
	if scope == ScopeNotify && len(stacks) > 0 {
		return "", t, errors.New("a notify token cannot be limited to stacks: it reads none")
	}
	if name == "" || strings.ContainsAny(name, "\n\r") {
		return "", t, errors.New("a token needs a name")
	}
	if ttl <= 0 {
		return "", t, errors.New("a token needs an expiry")
	}
	if scope == ScopeAdmin && len(stacks) > 0 {
		// Admin covers fleet-wide actions (tokens, settings) a stack limit cannot bound.
		return "", t, errors.New("an admin token cannot be limited to stacks; use scope apply")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return "", t, err
	}
	defer unlock()
	key, err := s.key(true)
	if err != nil {
		return "", t, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", t, err
	}
	secret = Prefix + base64.RawURLEncoding.EncodeToString(raw)
	id := make([]byte, 4)
	if _, err := rand.Read(id); err != nil {
		return "", t, err
	}
	now := s.now().UTC().Truncate(time.Second)
	t = Token{ID: hex.EncodeToString(id), Name: name, Hash: mac(key, secret), Scope: scope, Stacks: stacks, Created: now, Expires: now.Add(ttl)}
	all, err := s.load()
	if err != nil {
		return "", t, err
	}
	all = append(all, t)
	return secret, t, s.save(all)
}

// List returns the tokens, oldest first.
func (s *Store) List() ([]Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Revoke deletes a token by ID.
func (s *Store) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(all, func(t Token) bool { return t.ID == id })
	if i < 0 {
		return fmt.Errorf("no token %s", id)
	}
	return s.save(slices.Delete(all, i, i+1))
}

// ErrUnauthorized covers unknown, malformed and expired tokens alike.
var ErrUnauthorized = errors.New("unauthorized")

// Check returns the token a secret belongs to, if it is valid now.
func (s *Store) Check(secret string) (Token, error) {
	if !strings.HasPrefix(secret, Prefix) || len(secret) > 128 {
		return Token{}, ErrUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, err := s.key(false)
	if err != nil {
		return Token{}, ErrUnauthorized
	}
	all, err := s.load()
	if err != nil {
		return Token{}, ErrUnauthorized
	}
	h := mac(key, secret)
	for _, t := range all {
		if hmac.Equal([]byte(t.Hash), []byte(h)) {
			if !s.now().Before(t.Expires) {
				return Token{}, ErrUnauthorized
			}
			return t, nil
		}
	}
	return Token{}, ErrUnauthorized
}

func mac(key []byte, secret string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(secret))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Store) key(create bool) ([]byte, error) {
	path := filepath.Join(s.Dir, "token.key")
	k, err := os.ReadFile(path) //nolint:gosec // the installation's own state directory
	switch {
	case err == nil && len(k) == 32:
		return k, nil
	case err == nil:
		// Never replace a damaged key: every token would silently stop working.
		return nil, fmt.Errorf("token key %s is damaged (%d bytes, want 32); restore it from a backup, or delete it and tokens.json and create new tokens", path, len(k))
	case !create || !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("token key: %w", err)
	}
	k = make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	// Linked into place, never renamed over: of two first runs at once,
	// one key wins and the other run uses it.
	if err := writeNew(path, k); errors.Is(err, os.ErrExist) {
		return s.key(false)
	} else if err != nil {
		return nil, err
	}
	return k, nil
}

// lock holds an exclusive lock on the store across processes (CLI runs of
// create and revoke), so no update is lost. It returns the unlock.
func (s *Store) lock() (func(), error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	return flock.Lock(filepath.Join(s.Dir, ".lock"), true)
}

func (s *Store) load() ([]Token, error) {
	raw, err := os.ReadFile(filepath.Join(s.Dir, "tokens.json")) //nolint:gosec // the installation's own state directory
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all []Token
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("tokens.json: %w", err)
	}
	return all, nil
}

func (s *Store) save(all []Token) error {
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return writeFile(filepath.Join(s.Dir, "tokens.json"), raw)
}

// writeNew creates a file that must not exist yet, complete or not at all.
func writeNew(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Link(tmp.Name(), path)
}

// writeFile replaces a file atomically, owner-only.
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
