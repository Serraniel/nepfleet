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

// envCases were checked against `docker compose config` (Compose v2/v5).
var envCases = map[string]string{
	"A":   "1",
	"B":   "x $y",
	"C":   "line\nnext \"q\"",
	"D":   "plain",
	"E":   "",
	"Q":   "x",
	"U":   "xhiy",
	"DQ":  "hi there",
	"DF":  "fallback",
	"LIT": "$GREETING",
}

const envContent = `# c
export A=1
B='x $y'
C="line\nnext \"q\""
D=plain # comment
E=
GREETING=hi
Q="x" # trailing comment after a quoted value
U=x${GREETING}y
DQ="${GREETING} there"
DF=${UNSET:-fallback}
LIT='$GREETING'
`

func TestParseEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(envContent+"S=$FROM_SHELL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, fromShell, err := parseEnvFile(path, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range envCases {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(fromShell) != 1 || fromShell[0] != "FROM_SHELL" {
		t.Errorf("fromShell = %v", fromShell)
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
	cfg := "name: demo\nvolumes:\n  data: {name: demo_data}\n  shared: {name: media, external: true}\nservices:\n  web:\n    image: nginx\n    build: .\n    environment: {G: '${GREETING}', P: '${DB_PASSWORD}', S: '${FROM_SHELL}', D: '${WITH_DEFAULT:-x}'}\n"
	res, err := ImportCompose(context.Background(), fakeRunner{config: cfg}, f, store,
		Project{Name: "demo", ConfigFiles: []string{filepath.Join(work, "compose.yaml")}, WorkingDir: work, EnvFiles: []string{filepath.Join(work, ".env")}}, Options{Target: "nas"})
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
	if strings.Join(res.Volumes, ",") != "demo_data,media (external)" {
		t.Errorf("volumes = %v", res.Volumes)
	}
	w := strings.Join(res.Warnings, "\n")
	if !strings.Contains(w, "FROM_SHELL") || strings.Contains(w, "WITH_DEFAULT") || !strings.Contains(w, "built locally") {
		t.Errorf("warnings:\n%s", w)
	}
	if _, err := ImportCompose(context.Background(), fakeRunner{config: cfg}, f, store, Project{Name: "demo", ConfigFiles: []string{"x"}}, Options{Target: "nas"}); err == nil {
		t.Error("second import must refuse to overwrite")
	}
}

func TestSecretNames(t *testing.T) {
	for _, k := range []string{"DB_PWD", "MYSQL_ROOT_PW", "SMTP_PW", "JWT", "SESSION_SECRET", "COOKIE_KEY", "ADMIN_PIN", "API_TOKEN", "DATABASE_URL"} {
		if !secretName.MatchString(k) {
			t.Errorf("%s should be treated as secret", k)
		}
	}
	for _, k := range []string{"TZ", "GREETING", "PUID", "DOMAIN"} {
		if secretName.MatchString(k) {
			t.Errorf("%s should not be treated as secret", k)
		}
	}
}

func TestBadNameAndCleanup(t *testing.T) {
	f := &fleet.Fleet{Dir: t.TempDir()}
	if _, err := ImportCompose(context.Background(), fakeRunner{}, f, secrets.Memory{}, Project{Name: "bad_", ConfigFiles: []string{"x"}}, Options{}); err == nil {
		t.Error("invalid name accepted")
	}
	// A failing config step must not leave stacks/<name> behind.
	if _, err := ImportCompose(context.Background(), failRunner{}, f, secrets.Memory{}, Project{Name: "ok", ConfigFiles: []string{"x"}}, Options{}); err == nil {
		t.Error("expected failure")
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "stacks", "ok")); err == nil {
		t.Error("half-written stack left behind")
	}
}

type failRunner struct{}

func (failRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, os.ErrPermission
}
