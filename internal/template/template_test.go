// SPDX-License-Identifier: AGPL-3.0-or-later

package template

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
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

// The schema tags (internal/schema) must list what Validate accepts.
func TestSchemaTagsMatchValidation(t *testing.T) {
	for _, c := range []struct {
		v     any
		field string
		want  []string
	}{
		{Metadata{}, "Tier", tiers},
		{Image{}, "Track", tracks},
		{Input{}, "Type", inputTypes},
		{Generate{}, "Encoding", encodings[1:]},
		{Generate{}, "Kind", generateKinds},
		{Migration{}, "Backup", backups},
		{Migration{}, "Rollback", rollbacks},
	} {
		f, _ := reflect.TypeOf(c.v).FieldByName(c.field)
		if got := schemaEnum(f.Tag.Get("schema")); !slices.Equal(got, c.want) {
			t.Errorf("%T.%s: schema enum %v, Validate accepts %v", c.v, c.field, got, c.want)
		}
	}
	for _, c := range []struct {
		v     any
		field string
		re    *regexp.Regexp
	}{
		{Metadata{}, "Name", nameRe}, {Volume{}, "Name", nameRe}, {Input{}, "Name", envRe},
		{Generate{}, "Name", envRe}, {Spec{}, "Version", semverRe}, {Migration{}, "To", semverRe},
	} {
		f, _ := reflect.TypeOf(c.v).FieldByName(c.field)
		if !strings.Contains(f.Tag.Get("schema"), "pattern="+c.re.String()) {
			t.Errorf("%T.%s: schema pattern differs from %s", c.v, c.field, c.re)
		}
	}
}

func schemaEnum(tag string) []string {
	for part := range strings.SplitSeq(tag, ";") {
		if v, ok := strings.CutPrefix(part, "enum="); ok {
			return strings.Split(v, "|")
		}
	}
	return nil
}

func TestHardwareValidation(t *testing.T) {
	tpl, err := Load("testdata/demo")
	if err != nil {
		t.Fatal(err)
	}
	tpl.Spec.Inputs = append(tpl.Spec.Inputs, Input{Name: "ZIGBEE", Type: "device"}, Input{Name: "GPU", Type: "gpu"})
	tpl.Spec.Hardware = []Hardware{{Input: "ZIGBEE", Services: []string{"web"}, Target: "/dev/ttyUSB0"}, {Input: "GPU", Services: []string{"web"}}}
	if err := tpl.Validate(); err != nil {
		t.Fatalf("valid hardware rejected: %v", err)
	}
	tpl.Spec.Hardware = []Hardware{{Input: "DOMAIN", Services: []string{"web"}}, {Input: "GPU", Services: []string{"web"}, Target: "/dev/dri"}}
	err = tpl.Validate()
	if err == nil || !strings.Contains(err.Error(), "device or gpu") || !strings.Contains(err.Error(), "device inputs only") {
		t.Errorf("want both hardware errors, got %v", err)
	}
}

func TestMetricsLabels(t *testing.T) {
	for name, c := range map[string]struct {
		supports, labels, want string
	}{
		"valid":       {"ingress: true, metrics: true", `nepfleet.metrics.port: "9090", nepfleet.metrics.path: /m, nepfleet.metrics.scheme: https`, ""},
		"variable":    {"ingress: true, metrics: true", `nepfleet.metrics.port: "${PORT}"`, "${PORT}, which is neither"},
		"flag only":   {"ingress: true, metrics: true", ``, "supports.metrics is true, but no service has nepfleet.metrics.port labels"},
		"labels only": {"ingress: true", `nepfleet.metrics.port: "9090"`, "supports.metrics is false"},
		"bad port":    {"ingress: true, metrics: true", `nepfleet.metrics.port: "99999"`, "must be a port number"},
		"no port":     {"ingress: true, metrics: true", `nepfleet.metrics.path: /m`, "nepfleet.metrics.port is required"},
		"bad path":    {"ingress: true, metrics: true", `nepfleet.metrics.port: "1", nepfleet.metrics.path: m`, "must start with /"},
		"bad scheme":  {"ingress: true, metrics: true", `nepfleet.metrics.port: "1", nepfleet.metrics.scheme: ftp`, "http or https"},
		"unknown key": {"ingress: true, metrics: true", `nepfleet.metrics.port: "1", nepfleet.metrics.interval: 5s`, "unknown label nepfleet.metrics.interval"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := copyDemo(t)
			replaceIn(t, filepath.Join(dir, "template.yaml"), "supports: { ingress: true }", "supports: { "+c.supports+" }")
			if c.labels != "" {
				replaceIn(t, filepath.Join(dir, "compose.yaml"), "      caddy: ${DOMAIN}\n", "      caddy: ${DOMAIN}\n      "+strings.ReplaceAll(c.labels, ", n", "\n      n")+"\n")
			}
			tpl, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			err = tpl.Validate()
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("valid labels refused: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
}

func replaceIn(t *testing.T, path, old, repl string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), old) {
		t.Fatalf("%s lacks %q", path, old)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(raw), old, repl, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
}
