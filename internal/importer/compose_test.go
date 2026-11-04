// SPDX-License-Identifier: AGPL-3.0-or-later

package importer

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/secrets"
)

type fakeRunner struct{ config string }

func (f fakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if slices.Contains(args, "config") {
		return []byte(f.config), nil
	}
	return nil, nil
}

func TestParseEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# c\nexport A=1\nB='x $y'\nC=\"line\\nnext \\\"q\\\"\"\nD=plain # comment\nE=\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := parseEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "1", "B": "x $y", "C": "line\nnext \"q\"", "D": "plain", "E": ""}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestImportCompose(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "project")
	if err := os.MkdirAll(work, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".env"), []byte("GREETING=hi\nDB_PASSWORD=s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fleet.Fleet{Dir: filepath.Join(root, "fleet")}
	store := secrets.Memory{}
	cfg := "name: demo\nservices:\n  web:\n    image: nginx\n    build: .\n    environment: {G: '${GREETING}', P: '${DB_PASSWORD}', S: '${FROM_SHELL}', D: '${WITH_DEFAULT:-x}'}\n"
	res, err := ImportCompose(context.Background(), fakeRunner{config: cfg}, f, store,
		Project{Name: "demo", ConfigFiles: []string{filepath.Join(work, "compose.yaml")}, WorkingDir: work, EnvFiles: []string{filepath.Join(work, ".env")}}, "nas")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Secrets, ",") != "DB_PASSWORD" || strings.Join(res.Values, ",") != "GREETING" {
		t.Errorf("split: values=%v secrets=%v", res.Values, res.Secrets)
	}
	if store[filepath.Join(res.Dir, fleet.SecretsFile)]["DB_PASSWORD"] != "s3cr3t" {
		t.Error("secret not stored")
	}
	raw, _ := os.ReadFile(filepath.Join(res.Dir, "stack.yaml"))
	if strings.Contains(string(raw), "s3cr3t") || !strings.Contains(string(raw), "GREETING: hi") || !strings.Contains(string(raw), "target: nas") {
		t.Errorf("stack.yaml:\n%s", raw)
	}
	w := strings.Join(res.Warnings, "\n")
	if !strings.Contains(w, "FROM_SHELL") || strings.Contains(w, "WITH_DEFAULT") || !strings.Contains(w, "built locally") {
		t.Errorf("warnings:\n%s", w)
	}
	if _, err := ImportCompose(context.Background(), fakeRunner{config: cfg}, f, store, Project{Name: "demo", ConfigFiles: []string{"x"}}, "nas"); err == nil {
		t.Error("second import must refuse to overwrite")
	}
}
