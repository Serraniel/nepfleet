// SPDX-License-Identifier: AGPL-3.0-or-later

package secrets

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSOPSRoundTrip needs sops and age-keygen on PATH; it is skipped otherwise.
func TestSOPSRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sops"); err != nil {
		t.Skip("sops not installed")
	}
	if _, err := exec.LookPath("age-keygen"); err != nil {
		t.Skip("age-keygen not installed")
	}
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.txt")
	if out, err := exec.Command("age-keygen", "-o", keyFile).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen: %v %s", err, out)
	}
	pub, err := exec.Command("age-keygen", "-y", keyFile).Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", keyFile)

	s := SOPS{Recipients: []string{strings.TrimSpace(string(pub))}}
	path := filepath.Join(dir, "values.secret.yaml")
	in := map[string]string{"ADMIN_TOKEN": "$argon2id$v=19$m=65540", "DB_PASSWORD": "p'w\"d"}
	if err := s.Save(path, in); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "argon2id") || !IsEncrypted(raw) {
		t.Fatalf("file is not encrypted:\n%s", raw)
	}
	out, err := s.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("%s = %q, want %q", k, out[k], v)
		}
	}
}

func TestRefusesPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.secret.yaml")
	if err := os.WriteFile(path, []byte("PASSWORD: hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (SOPS{}).Load(path); err == nil || !strings.Contains(err.Error(), "not SOPS-encrypted") {
		t.Fatalf("expected refusal, got %v", err)
	}
}

func TestMissingFileIsEmpty(t *testing.T) {
	m, err := (SOPS{}).Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil || len(m) != 0 {
		t.Fatalf("got %v, %v", m, err)
	}
}
