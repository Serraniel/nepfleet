// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"errors"
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
