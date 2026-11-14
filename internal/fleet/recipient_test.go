// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAddRecipient(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	const pub = "age17nyl33ndnkt7dctqjrn3pefmjetgznrk3tpqpkaqpr7m73jzhqqssrnltg"
	if added, err := AddRecipient(dir, pub); err != nil || !added {
		t.Fatalf("add: %v %v", added, err)
	}
	f, err := Load(dir)
	if err != nil || !slices.Equal(f.Spec.Secrets.Age, []string{pub}) {
		t.Fatalf("recipients: %v %v", f.Spec.Secrets.Age, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "fleet.yaml"))
	if !strings.Contains(string(raw), "# Secrets are committed encrypted") {
		t.Error("the comments went")
	}
	if added, err := AddRecipient(dir, pub); err != nil || added {
		t.Errorf("again: %v %v", added, err)
	}
	if _, err := AddRecipient(dir, "age1short"); err == nil {
		t.Error("not a key")
	}
}
