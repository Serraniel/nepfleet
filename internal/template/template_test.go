// SPDX-License-Identifier: AGPL-3.0-or-later

package template

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidTemplate(t *testing.T) {
	tpl, err := Load("testdata/demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := tpl.Validate(); err != nil {
		t.Fatalf("expected valid template, got:\n%v", err)
	}
}

func TestUnknownFieldIsRejected(t *testing.T) {
	dir := copyDemo(t)
	appendFile(t, filepath.Join(dir, "template.yaml"), "surprise: true\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("expected strict decoding to reject an unknown field")
	}
}

func TestUndeclaredComposeVariable(t *testing.T) {
	dir := copyDemo(t)
	appendFile(t, filepath.Join(dir, "compose.yaml"), "x-extra: ${UNDECLARED}\n")
	tpl, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = tpl.Validate()
	if err == nil || !strings.Contains(err.Error(), "UNDECLARED") {
		t.Fatalf("expected an error naming UNDECLARED, got %v", err)
	}
	if strings.Contains(err.Error(), "NOT_A_VARIABLE") {
		t.Fatalf("an escaped $${VAR} was treated as interpolation: %v", err)
	}
}

func TestImagePolicyMustMatchCompose(t *testing.T) {
	dir := copyDemo(t)
	path := filepath.Join(dir, "template.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "  images:\n", "  images:\n    - name: redis\n      track: minor\n", 1))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tpl, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := tpl.Validate(); err == nil || !strings.Contains(err.Error(), `"redis" is not used`) {
		t.Fatalf("expected unused-policy error, got %v", err)
	}
}

func TestSupportsMustMatchLabels(t *testing.T) {
	dir := copyDemo(t)
	path := filepath.Join(dir, "template.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "supports: { ingress: true }", "supports: { monitoring: true }", 1))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tpl, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = tpl.Validate()
	if err == nil || !strings.Contains(err.Error(), "supports.monitoring is true, but no service has kuma.* labels") ||
		!strings.Contains(err.Error(), "caddy labels, but supports.ingress is false") {
		t.Fatalf("expected both directions to fail, got %v", err)
	}
}

func TestImageName(t *testing.T) {
	for in, want := range map[string]string{"nginx:1": "nginx", "localhost:5000/a:1@sha256:x": "localhost:5000/a", "ghcr.io/a/b": "ghcr.io/a/b"} {
		if got := ImageName(in); got != want {
			t.Errorf("ImageName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImageRefs(t *testing.T) {
	for ref, ok := range map[string]bool{
		"nginx:1.29":            true,
		"ghcr.io/a/b:2":         true,
		"registry:5000/a/b:1.0": true,
		"nginx@sha256:abc":      true,
		"nginx":                 false,
		"nginx:latest":          false,
		"registry:5000/a/b":     false,
	} {
		if err := checkImageRef(ref); (err == nil) != ok {
			t.Errorf("checkImageRef(%q) = %v, want ok=%v", ref, err, ok)
		}
	}
}

func TestGenerate(t *testing.T) {
	for _, enc := range []string{"hex", "base64url", "alnum", ""} {
		g := Generate{Name: "X", Kind: "secret", Length: 33, Encoding: enc}
		a, err := g.Value()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := g.Value()
		if len(a) != 33 || a == b {
			t.Errorf("encoding %q: got %q and %q", enc, a, b)
		}
	}
}

func copyDemo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "demo")
	if err := os.CopyFS(dir, os.DirFS("testdata/demo")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
