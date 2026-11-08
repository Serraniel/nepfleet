// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestAddCredentialRefusesDuplicates(t *testing.T) {
	s := &UserStore{Dir: t.TempDir()}
	ada, err := NewUser("Ada")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := NewUser("Bob")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []*User{ada, bob} {
		if err := s.Add(u, false); err != nil {
			t.Fatal(err)
		}
	}
	key := webauthn.Credential{ID: []byte("key-1")}
	if err := s.AddCredential(ada.ID, key); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCredential(bob.ID, key); err == nil {
		t.Error("one passkey registered to two people")
	}
	if err := s.AddCredential(ada.ID, key); err == nil {
		t.Error("the same passkey twice")
	}
	if err := s.AddCredential(ada.ID, webauthn.Credential{ID: []byte("key-2")}); err != nil {
		t.Error(err)
	}
	if err := s.AddCredential([]byte("nobody"), webauthn.Credential{ID: []byte("key-3")}); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("%v", err)
	}
	got, err := s.ByID(ada.ID)
	if err != nil || len(got.Credentials) != 2 {
		t.Errorf("%+v %v", got, err)
	}
}

func TestLockAndRemoveCredential(t *testing.T) {
	s := &UserStore{Dir: t.TempDir()}
	ada, _ := NewUser("Ada")
	if err := s.Add(ada, false); err != nil {
		t.Fatal(err)
	}
	one, two := webauthn.Credential{ID: []byte("credential-one")}, webauthn.Credential{ID: []byte("credential-two")}
	for _, c := range []webauthn.Credential{one, two} {
		if err := s.AddCredential(ada.ID, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.LockCredential(ada.ID, one.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.LockCredential(ada.ID, one.ID); err != nil {
		t.Fatal(err)
	}
	u, _ := s.ByID(ada.ID)
	if !u.IsLocked(one.ID) || u.IsLocked(two.ID) || len(u.Locked) != 1 {
		t.Errorf("locked: %+v", u.Locked)
	}
	if err := s.RemoveCredential("Ada", "Y3JlZGVudGlhbC"); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Errorf("an ambiguous prefix: %v", err)
	}
	if err := s.RemoveCredential("Ada", "nope"); err == nil {
		t.Error("an unknown passkey")
	}
	if err := s.RemoveCredential("Bob", CredentialName(one.ID)); err == nil {
		t.Error("an unknown user")
	}
	if err := s.RemoveCredential("Ada", base64.RawURLEncoding.EncodeToString(one.ID)); err != nil {
		t.Fatal(err)
	}
	u, _ = s.ByID(ada.ID)
	if len(u.Credentials) != 1 || string(u.Credentials[0].ID) != "credential-two" || len(u.Locked) != 0 {
		t.Errorf("after removing: %+v", u)
	}
	if err := s.RemoveCredential("Ada", base64.RawURLEncoding.EncodeToString(two.ID)); err == nil || !strings.Contains(err.Error(), "last way") {
		t.Errorf("the last way in: %v", err)
	}
	if err := s.update(ada.ID, func(u *User) { u.TOTP = &TOTP{} }); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveCredential("Ada", base64.RawURLEncoding.EncodeToString(two.ID)); err != nil {
		t.Errorf("with codes on, the last passkey can go: %v", err)
	}
}
