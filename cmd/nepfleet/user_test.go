// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Serraniel/nepfleet/internal/auth"
)

func TestUserPasskeys(t *testing.T) {
	state := t.TempDir()
	store := &auth.UserStore{Dir: filepath.Join(state, "api")}
	ada, _ := auth.NewUser("Ada")
	if err := store.Add(ada, false); err != nil {
		t.Fatal(err)
	}
	one, two := []byte("passkey-number-one"), []byte("other-passkey-two")
	for _, id := range [][]byte{one, two} {
		if err := store.AddCredential(ada.ID, webauthn.Credential{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.LockCredential(ada.ID, one); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := cmdUser([]string{"passkeys", "Ada", "--state", state}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), auth.CredentialName(one)) || !strings.Contains(out.String(), "LOCKED") || strings.Count(out.String(), "\n") != 2 {
		t.Errorf("list:\n%s", out.String())
	}
	out.Reset()
	if err := cmdUser([]string{"remove-passkey", "Ada", base64.RawURLEncoding.EncodeToString(one), "--state", state}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	u, _ := store.ByID(ada.ID)
	if len(u.Credentials) != 1 || len(u.Locked) != 0 {
		t.Errorf("after removing: %+v", u)
	}
	if err := cmdUser([]string{"passkeys", "Bob", "--state", state}, &out, &errOut); err == nil {
		t.Error("an unknown user")
	}
}
