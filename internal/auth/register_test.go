// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"strings"
	"testing"
	"time"
)

const stackSecret = Prefix + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestRegister(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := &Store{Dir: t.TempDir(), Now: func() time.Time { return now }}

	if _, known := s.ValidFor(stackSecret, 0); known {
		t.Error("a store without a key can't know a token")
	}
	if _, err := s.Register("stack:dozzle/T", ScopeNotify, stackSecret, 365*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if tok, err := s.Check(stackSecret); err != nil || tok.Scope != ScopeNotify {
		t.Fatalf("registered token: %v %v", tok, err)
	}
	if valid, known := s.ValidFor(stackSecret, 60*24*time.Hour); !valid || !known {
		t.Errorf("fresh token: valid %v known %v", valid, known)
	}
	if valid, known := s.ValidFor(stackSecret, 400*24*time.Hour); valid || !known {
		t.Errorf("expiring within the window: valid %v known %v", valid, known)
	}
	if valid, known := s.ValidFor(Prefix+"other", 0); valid || !known {
		t.Errorf("unknown token: valid %v known %v", valid, known)
	}

	// A renewal: the old token of the same name ends within the grace period
	// (a rollback to the files that hold it still works meanwhile).
	renewed := Prefix + strings.Repeat("B", 43)
	if _, err := s.Register("stack:dozzle/T", ScopeNotify, renewed, 365*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(stackSecret); err != nil {
		t.Error("the previous token stopped at once; a rollback would break")
	}
	now = now.Add(RenewGrace)
	if _, err := s.Check(stackSecret); err == nil {
		t.Error("the previous token outlived the grace period")
	}
	if _, err := s.Check(renewed); err != nil {
		t.Errorf("the renewed token: %v", err)
	}
	// The next registration drops tokens of that name that have ended.
	if _, err := s.Register("stack:dozzle/T", ScopeNotify, Prefix+strings.Repeat("C", 43), time.Hour); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.List(); len(all) != 2 {
		t.Errorf("tokens = %d, want 2 (the renewed one in its grace, and the new one)", len(all))
	}

	for _, bad := range []struct{ scope, secret string }{
		{ScopeAdmin, renewed},         // a stack never holds admin
		{ScopeNotify, "short"},        // not a token
		{ScopeNotify, Prefix + "abc"}, // too short to be one we made
		{"root", renewed},             // no such scope
	} {
		if _, err := s.Register("stack:x/T", bad.scope, bad.secret, time.Hour); err == nil {
			t.Errorf("Register(%q, %q) accepted", bad.scope, bad.secret)
		}
	}
}
