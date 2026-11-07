// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Serraniel/nepfleet/internal/flock"
)

// User is a person who signs in with passkeys, or codes as a fallback.
type User struct {
	ID          []byte                `json:"id"`
	Name        string                `json:"name"`
	Credentials []webauthn.Credential `json:"credentials"`
	TOTP        *TOTP                 `json:"totp,omitempty"`
	Created     time.Time             `json:"created"`
	// External users exist through an identity provider, which decides if they
	// may come in: a passkey or code of their own would get around that.
	External bool `json:"external,omitempty"`
}

// WebAuthnID and the rest make a User what the passkey library works with.
func (u *User) WebAuthnID() []byte { return u.ID }

// WebAuthnName is the account name a passkey shows.
func (u *User) WebAuthnName() string { return u.Name }

// WebAuthnDisplayName is the same name.
func (u *User) WebAuthnDisplayName() string { return u.Name }

// WebAuthnCredentials are the user's passkeys.
func (u *User) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

// UserStore keeps users in <dir>/users.json, owner-only. It holds public keys, and
// the secrets of users who turned on sign-in codes.
type UserStore struct {
	Dir string
	mu  sync.Mutex
}

var userNameRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{M}\p{N} ._-]{0,62}$`)

// ValidUserName checks a name: it ends up as a commit's author.
func ValidUserName(s string) bool { return userNameRe.MatchString(s) }

func (s *UserStore) lock() (func(), error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	return flock.Lock(filepath.Join(s.Dir, ".users.lock"), true)
}

func (s *UserStore) load() ([]User, error) {
	raw, err := os.ReadFile(filepath.Join(s.Dir, "users.json")) //nolint:gosec // the installation's own state directory
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all []User
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("users.json: %w", err)
	}
	return all, nil
}

func (s *UserStore) save(all []User) error {
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(s.Dir, "users.json"), raw)
}

// Any reports whether a user exists. A damaged file counts as yes: it must not
// reopen setup.
func (s *UserStore) Any() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	return err != nil || len(all) > 0
}

// List returns every user.
func (s *UserStore) List() ([]User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// ByID finds a user by the handle a passkey carries.
func (s *UserStore) ByID(id []byte) (*User, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if subtle.ConstantTimeCompare(all[i].ID, id) == 1 {
			return &all[i], nil
		}
	}
	return nil, ErrUnauthorized
}

// NewUser makes a user that isn't stored yet.
func NewUser(name string) (*User, error) {
	if !ValidUserName(name) {
		return nil, errors.New("a name is letters, digits, spaces and ._-, at most 63")
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	return &User{ID: id, Name: name, Created: time.Now().UTC().Truncate(time.Second)}, nil
}

// Add stores a user. firstOnly refuses if any exist: setup creates one user, even
// if two requests race with the same token.
func (s *UserStore) Add(u *User, firstOnly bool) error {
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
	if firstOnly && len(all) > 0 {
		return errors.New("setup is done")
	}
	if slices.ContainsFunc(all, func(o User) bool { return o.Name == u.Name }) {
		return errors.New("that name exists")
	}
	return s.save(append(all, *u))
}

// EnsureExternal returns the user an identity provider vouches for, creating them
// the first time. The ID says who someone is; a name that's taken gets a number,
// so nobody signs in as someone else who happens to share it.
func (s *UserStore) EnsureExternal(id []byte, name string) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if subtle.ConstantTimeCompare(all[i].ID, id) == 1 {
			return &all[i], nil
		}
	}
	taken := func(n string) bool {
		return slices.ContainsFunc(all, func(u User) bool { return u.Name == n })
	}
	unique := name
	for n := 2; taken(unique); n++ {
		suffix := fmt.Sprintf("-%d", n)
		base := []rune(name)
		// Room for the number: names have a maximum length.
		base = base[:min(len(base), 63-len(suffix))]
		unique = string(base) + suffix
	}
	if !ValidUserName(unique) {
		return nil, fmt.Errorf("%q can't be a user name", unique)
	}
	u := User{ID: id, Name: unique, Created: time.Now().UTC().Truncate(time.Second), External: true}
	if err := s.save(append(all, u)); err != nil {
		return nil, err
	}
	return &u, nil
}

// AddCredential gives a user another passkey.
func (s *UserStore) AddCredential(id []byte, c webauthn.Credential) error {
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
	// A credential belongs to one person: it says who is signing in.
	for i := range all {
		for j := range all[i].Credentials {
			if subtle.ConstantTimeCompare(all[i].Credentials[j].ID, c.ID) == 1 {
				return errors.New("that passkey is registered already")
			}
		}
	}
	for i := range all {
		if subtle.ConstantTimeCompare(all[i].ID, id) == 1 {
			all[i].Credentials = append(all[i].Credentials, c)
			return s.save(all)
		}
	}
	return ErrUnauthorized
}

// UpdateCredential records a passkey's new signature count after a login.
func (s *UserStore) UpdateCredential(id []byte, c webauthn.Credential) error {
	return s.update(id, func(u *User) {
		for i := range u.Credentials {
			if subtle.ConstantTimeCompare(u.Credentials[i].ID, c.ID) == 1 {
				u.Credentials[i] = c
			}
		}
	})
}

func (s *UserStore) update(id []byte, change func(*User)) error {
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
	for i := range all {
		if subtle.ConstantTimeCompare(all[i].ID, id) == 1 {
			change(&all[i])
			return s.save(all)
		}
	}
	return ErrUnauthorized
}

// Remove deletes a user by name.
func (s *UserStore) Remove(name string) error {
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
	i := slices.IndexFunc(all, func(u User) bool { return u.Name == name })
	if i < 0 {
		return fmt.Errorf("no user %q", name)
	}
	return s.save(slices.Delete(all, i, i+1))
}

// SessionLifetime is how long a sign-in lasts.
const SessionLifetime = 12 * time.Hour

// Sessions are sign-ins, in memory only: a restart signs everyone out, and no
// secret that grants access lies on disk. Only a hash of each ID is kept.
type Sessions struct {
	mu  sync.Mutex
	all map[[32]byte]session
	Now func() time.Time
}

type session struct {
	userID  []byte
	name    string
	expires time.Time
}

func (s *Sessions) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Create starts a session and returns its secret ID for the cookie.
func (s *Sessions) Create(u *User) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.all == nil {
		s.all = map[[32]byte]session{}
	}
	now := s.now()
	for k, v := range s.all {
		if now.After(v.expires) {
			delete(s.all, k)
		}
	}
	s.all[sha256.Sum256([]byte(id))] = session{userID: u.ID, name: u.Name, expires: now.Add(SessionLifetime)}
	return id, nil
}

// Check returns the user a session belongs to.
func (s *Sessions) Check(id string) (userID []byte, name string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := sha256.Sum256([]byte(id))
	v, found := s.all[k]
	if !found {
		return nil, "", false
	}
	if s.now().After(v.expires) {
		delete(s.all, k)
		return nil, "", false
	}
	return v.userID, v.name, true
}

// End signs a session out.
func (s *Sessions) End(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.all, sha256.Sum256([]byte(id)))
}
