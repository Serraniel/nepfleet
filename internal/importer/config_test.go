// SPDX-License-Identifier: AGPL-3.0-or-later

package importer

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/render"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func composeWith(sources ...string) []byte {
	var b strings.Builder
	b.WriteString("services:\n  app:\n    image: x\n    volumes:\n")
	for _, s := range sources {
		b.WriteString("      - type: bind\n        source: " + s + "\n        target: /c\n        read_only: true\n")
	}
	return []byte(b.String())
}

// What matters most: after the move, a stack renders the very same files.
func TestCopyConfigRendersTheSame(t *testing.T) {
	src := filepath.Join(t.TempDir(), "homepage")
	originals := map[string]string{
		"settings.yaml": "title: Home\napi_key: abcd1234efgh\nurl: ${SOMETHING}\nliteral: $${x}\n",
		"sub/.env":      "DB_PASSWORD='s3cret-value'\nPORT=8080\napiVersion: 1.2.3\nTOKEN=${ALREADY}\n",
	}
	writeTree(t, src, originals)
	secrets := map[string]string{}
	cfg, files, warnings, err := copyConfig(composeWith(src), []string{src}, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "source: ./config/homepage") || strings.Contains(string(cfg), src) {
		t.Errorf("compose file: %s", cfg)
	}
	for _, name := range []string{"CONFIG_API_KEY", "CONFIG_DB_PASSWORD"} {
		if secrets[name] == "" {
			t.Errorf("%s wasn't moved: %v", name, secrets)
		}
	}
	for rel, raw := range files {
		if strings.Contains(string(raw), "abcd1234efgh") || strings.Contains(string(raw), "s3cret-value") {
			t.Errorf("%s still holds a secret: %s", rel, raw)
		}
	}
	if len(warnings) != 2 {
		t.Errorf("each moved value should be named: %v", warnings)
	}

	dir := t.TempDir()
	if err := writeConfigFiles(dir, files); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	st := &fleet.Stack{Dir: dir}
	st.Metadata.Name = "app"
	// A value named like something a file mentions must not leak into it.
	st.Spec.Values = fleet.Literals(map[string]string{"SOMETHING": "changed", "x": "changed"})
	out, err := render.Render(render.Input{Fleet: &fleet.Fleet{}, Stack: st, Secrets: maps.Clone(secrets)})
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range originals {
		if got := string(out.Files["config/homepage/"+rel]); got != want {
			t.Errorf("%s renders as:\n%s\nwant:\n%s", rel, got, want)
		}
	}
}

func TestCopyConfigSingleFileAndNames(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a", "app.yaml"), filepath.Join(root, "b", "app.yaml")
	writeTree(t, root, map[string]string{"a/app.yaml": "x: 1\n", "b/app.yaml": "y: 2\n"})
	cfg, files, _, err := copyConfig(composeWith(a, b), []string{a, b}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if string(files["config/app.yaml"]) != "x: 1\n" || string(files["config/app.yaml-2"]) != "y: 2\n" {
		t.Errorf("%v", files)
	}
	if !strings.Contains(string(cfg), "./config/app.yaml-2") {
		t.Errorf("%s", cfg)
	}
}

func TestCopyConfigWarnsAboutWritableMounts(t *testing.T) {
	src := filepath.Join(t.TempDir(), "conf")
	writeTree(t, src, map[string]string{"a": "x\n"})
	writable := strings.ReplaceAll(string(composeWith(src)), "        read_only: true\n", "")
	_, _, warnings, err := copyConfig([]byte(writable), []string{src}, map[string]string{})
	if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "overwritten") {
		t.Errorf("%v %v", warnings, err)
	}
}

func TestCopyConfigRefusesData(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"bin/x": "a\x00b", "big/x": strings.Repeat("a", render.MaxStackFile+1)})
	link := filepath.Join(root, "link")
	if err := os.MkdirAll(link, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(link, "x")); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"bin", "big", "link", "missing"} {
		p := filepath.Join(root, sub)
		if _, _, _, err := copyConfig(composeWith(p), []string{p}, map[string]string{}); err == nil {
			t.Errorf("%s must be refused", sub)
		}
	}
	if _, _, _, err := copyConfig(composeWith("/elsewhere"), []string{filepath.Join(root, "bin")}, map[string]string{}); err == nil {
		t.Error("a path no service mounts must be refused")
	}
}

func TestMoveConfigSecretsLeavesOrdinaryValues(t *testing.T) {
	for _, line := range []string{
		"name: Home", "token: short", "token: true", "auth_url: https://id.example.org/x",
		"key_path: /etc/ssl/key.pem", "# password: commented-out", "version: 1.2.3.4.5",
		`password: "mismatched'`,
	} {
		secrets := map[string]string{}
		out, moved := moveConfigSecrets([]byte(line), secrets)
		if len(moved) != 0 || string(out) != line {
			t.Errorf("%q: %s %v", line, out, moved)
		}
	}
	// One value under two keys of the same name is one secret; two values aren't.
	secrets := map[string]string{}
	_, moved := moveConfigSecrets([]byte("token: aaaaaaaaaa\ntoken: aaaaaaaaaa\ntoken: bbbbbbbbbb\n"), secrets)
	if len(secrets) != 2 || len(moved) != 3 || secrets["CONFIG_TOKEN_2"] != "bbbbbbbbbb" {
		t.Errorf("%v %v", secrets, moved)
	}
}

func TestConfigCandidates(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"conf/a": "x\n", "data/big": strings.Repeat("a", render.MaxStackFile+1), "done/a": "x\n"})
	cfg := composeWith(filepath.Join(root, "conf"), filepath.Join(root, "data"), filepath.Join(root, "done"), "/etc/hostname", "/nonexistent")
	got := configCandidates(cfg, []string{filepath.Join(root, "done")})
	if len(got) != 1 || got[0] != filepath.Join(root, "conf") {
		t.Errorf("%v", got)
	}
}
