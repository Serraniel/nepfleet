// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokens(t *testing.T) {
	now := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	s := &Store{Dir: t.TempDir(), Now: func() time.Time { return now }}
	secret, tok, err := s.Create("ci", ScopeApply, []string{"web"}, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, Prefix) || len(secret) < 40 {
		t.Errorf("secret = %q", secret)
	}
	got, err := s.Check(secret)
	if err != nil || got.ID != tok.ID {
		t.Fatalf("check: %+v %v", got, err)
	}
	if !got.Allows(ScopeRead, "web") || !got.Allows(ScopeApply, "web") || got.Allows(ScopeAdmin, "") || got.Allows(ScopeRead, "db") {
		t.Errorf("scopes: %+v", got)
	}
	for _, bad := range []string{"", "nft_", secret + "x", strings.TrimPrefix(secret, Prefix), strings.Repeat("a", 200)} {
		if _, err := s.Check(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// The secret is not stored, and the files are owner-only.
	for _, f := range []string{"tokens.json", "token.key"} {
		raw, err := os.ReadFile(filepath.Join(s.Dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), secret) {
			t.Errorf("%s holds the secret", f)
		}
		if fi, _ := os.Stat(filepath.Join(s.Dir, f)); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v", f, fi.Mode().Perm())
		}
	}
	now = now.Add(25 * time.Hour)
	if _, err := s.Check(secret); err == nil {
		t.Error("an expired token must fail")
	}
	if err := s.Revoke(tok.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.List(); len(all) != 0 {
		t.Errorf("after revoke: %v", all)
	}
	if _, _, err := s.Create("x", "root", nil, time.Hour); err == nil {
		t.Error("an unknown scope must fail")
	}
	if _, _, err := s.Create("x", ScopeRead, nil, 0); err == nil {
		t.Error("a token without expiry must fail")
	}
}
