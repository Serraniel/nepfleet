// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/api"
)

// A rollback fleet whose secrets encrypt to a fresh age key.
func secretFleet(t *testing.T) (common, string) {
	t.Helper()
	for _, tool := range []string{"sops", "age-keygen"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is not installed")
		}
	}
	key := filepath.Join(t.TempDir(), "key.txt")
	if out, err := exec.Command("age-keygen", "-o", key).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	pub, err := exec.Command("age-keygen", "-y", key).Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", key)
	c, dir, _ := rollbackFleet(t)
	fy := filepath.Join(dir, "fleet.yaml")
	raw, err := os.ReadFile(fy) //nolint:gosec // the test's fleet
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "age: []", "age: ["+strings.TrimSpace(string(pub))+"]", 1))
	if err := os.WriteFile(fy, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tgit(t, dir, "commit", "-qam", "age", "--no-verify")
	return c, dir
}

func TestSecretsAPI(t *testing.T) {
	c, dir := secretFleet(t)
	commit, err := setSecretAPI(c, "web", "DB_PASSWORD", new("hunter2-long"), "ci")
	if err != nil {
		t.Fatal(err)
	}
	if head := strings.TrimSpace(tgit(t, dir, "rev-parse", "HEAD")); head != commit {
		t.Errorf("commit %s, HEAD %s", commit, head)
	}
	if author := tgit(t, dir, "log", "-1", "--format=%an %s"); !strings.Contains(author, "ci chore(web): set secret DB_PASSWORD") {
		t.Errorf("commit: %s", author)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "stacks/web/values.secret.yaml"))
	if strings.Contains(string(raw), "hunter2-long") {
		t.Fatal("stored in plain text")
	}
	if _, err := setSecretAPI(c, "", "KUMA_PASSWORD", new("x"), "ci"); err != nil {
		t.Fatal(err)
	}
	keys, err := secretKeys(c, "web")
	if err != nil || !slices.Equal(keys, []string{"DB_PASSWORD"}) {
		t.Errorf("keys: %v %v", keys, err)
	}
	if keys, _ := secretKeys(c, ""); !slices.Equal(keys, []string{"KUMA_PASSWORD"}) {
		t.Errorf("fleet keys: %v", keys)
	}
	if _, err := setSecretAPI(c, "web", "NOPE", nil, "ci"); !errors.Is(err, api.ErrNoSecret) {
		t.Errorf("removing a missing key: %v", err)
	}
	if _, err := setSecretAPI(c, "web", "DB_PASSWORD", nil, "ci"); err != nil {
		t.Fatal(err)
	}
	if keys, _ := secretKeys(c, "web"); len(keys) != 0 {
		t.Errorf("after removing: %v", keys)
	}
	if _, err := setSecretAPI(c, "ghost", "K", new("v"), "ci"); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("unknown stack: %v", err)
	}
	// Uncommitted changes on the server stay theirs.
	path := filepath.Join(dir, "stacks/web/values.secret.yaml")
	if err := os.WriteFile(path, []byte("hand edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := setSecretAPI(c, "web", "K", new("v"), "ci"); !errors.Is(err, api.ErrUncommitted) {
		t.Errorf("dirty file: %v", err)
	}
}

func TestFeatureList(t *testing.T) {
	c, _ := newStackFleet(t)
	fs, err := featureList(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != len(features) || fs[0].Name != "ingress" || fs[0].Stacks == nil {
		t.Errorf("features: %+v", fs)
	}
}

func TestSBOMAPI(t *testing.T) {
	c, _, _ := rollbackFleet(t)
	yes := func(string) bool { return true }
	doc, err := sbomAPI(t.Context(), c, "web", yes, false)
	if err != nil || doc == nil {
		t.Fatalf("stack: %v", err)
	}
	if _, err := sbomAPI(t.Context(), c, "web", func(string) bool { return false }, false); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("a stack the token doesn't see: %v", err)
	}
	fleetDoc, err := sbomAPI(t.Context(), c, "", yes, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(fleetDoc)
	if !strings.Contains(string(raw), "nginx") {
		t.Errorf("fleet document: %s", raw)
	}
}
