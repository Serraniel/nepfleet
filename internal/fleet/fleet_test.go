// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestHookBlocksPlaintextSecrets runs the real hook in a real repository.
func TestHookBlocksPlaintextSecrets(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		return cmd.Run()
	}
	secret := filepath.Join(dir, "stacks", "values.secret.yaml")
	if err := os.WriteFile(secret, []byte("PASSWORD: hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run("add", "-A"); err != nil {
		t.Fatal(err)
	}
	if err := run("commit", "-qm", "plaintext"); err == nil {
		t.Fatal("hook let a plaintext secret through")
	}
	if err := os.WriteFile(secret, []byte("PASSWORD: ENC[AES256_GCM,data:x]\nsops:\n  version: 3.11.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run("add", "-A"); err != nil {
		t.Fatal(err)
	}
	if err := run("commit", "-qm", "encrypted"); err != nil {
		t.Fatalf("hook rejected an encrypted file: %v", err)
	}
	// A plaintext key added to an encrypted file, under a non-ASCII name with a space.
	odd := filepath.Join(dir, "stacks", "ä b.secret.yaml")
	if err := os.WriteFile(odd, []byte("PASSWORD: ENC[AES256_GCM,data:x]\nNEW: plaintext\nsops:\n  version: 3.11.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run("add", "-A"); err != nil {
		t.Fatal(err)
	}
	if err := run("commit", "-qm", "partly plaintext"); err == nil {
		t.Fatal("hook let a partly plaintext, non-ASCII-named secret through")
	}
	if err := run("reset", "-q"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(odd); err != nil {
		t.Fatal(err)
	}
	// A value on the next, indented line is still a plaintext value.
	if err := os.WriteFile(secret, []byte("KEY:\n  hunter2\nsops:\n  version: 3.11.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run("add", "-A"); err != nil {
		t.Fatal(err)
	}
	if err := run("commit", "-qm", "multiline plaintext"); err == nil {
		t.Fatal("hook let a value on the next line through")
	}
}

func TestUnencryptedSecrets(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "stacks", "a"), 0o750)
	_ = os.WriteFile(filepath.Join(dir, "stacks", "a", "values.secret.yaml"), []byte("X: y\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "stacks", "a", "other.yaml"), []byte("X: y\n"), 0o600)
	bad, err := UnencryptedSecrets(dir, func(b []byte) bool { return strings.Contains(string(b), "sops:") })
	if err != nil || len(bad) != 1 || bad[0] != filepath.Join("stacks", "a", "values.secret.yaml") {
		t.Fatalf("got %v, %v", bad, err)
	}
}

func TestInit(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"fleet.yaml", ".sops.yaml", "stacks/.gitkeep", ".git"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, hookPath)); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("pre-commit hook missing or not executable: %v", err)
	}
	if out, _ := exec.Command("git", "-C", dir, "config", "core.hooksPath").Output(); strings.TrimSpace(string(out)) != ".githooks" {
		t.Errorf("core.hooksPath = %q", out)
	}
	if err := Init(dir); !errors.Is(err, ErrExists) {
		t.Fatalf("second Init = %v, want ErrExists", err)
	}
}

// The schema tags (internal/schema) must list what the parser accepts.
func TestSchemaTagsMatchValidation(t *testing.T) {
	enum := func(v any, path ...string) []string {
		ty := reflect.TypeOf(v)
		var f reflect.StructField
		for _, p := range path {
			f, _ = ty.FieldByName(p)
			ty = f.Type
		}
		for part := range strings.SplitSeq(f.Tag.Get("schema"), ";") {
			if v, ok := strings.CutPrefix(part, "enum="); ok {
				return strings.Split(v, "|")
			}
		}
		return nil
	}
	for _, c := range []struct {
		got, want []string
		name      string
	}{
		{enum(StackSpec{}, "Mode"), modes, "spec.mode"},
		{enum(StackSpec{}, "Apply"), applyPolicies, "spec.apply"},
		{enum(StackSpec{}, "Updates"), updatePolicies, "spec.updates"},
		{enum(StackSpec{}, "Snapshot"), snapshotModes, "spec.snapshot"},
		{enum(Spec{}, "Updates", "Images"), updatePolicies, "fleet spec.updates.images"},
		{enum(Spec{}, "Updates", "Host"), updatePolicies, "fleet spec.updates.host"},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s: schema enum %v, parser accepts %v", c.name, c.got, c.want)
		}
	}
	for _, c := range []struct {
		v    any
		path []string
	}{{Source{}, []string{"Name"}}, {Stack{}, []string{"Metadata", "Name"}}, {Fleet{}, []string{"Metadata", "Name"}}} {
		ty := reflect.TypeOf(c.v)
		var f reflect.StructField
		for _, p := range c.path {
			f, _ = ty.FieldByName(p)
			ty = f.Type
		}
		if !strings.Contains(f.Tag.Get("schema"), "pattern="+nameRe.String()) {
			t.Errorf("%T.%v: name pattern differs from %s", c.v, c.path, nameRe)
		}
	}
}

func TestSharedPaths(t *testing.T) {
	for raw, ok := range map[string]bool{
		"media: /mnt/media": true,
		"media: mnt/rel":         false,
		"media: /mnt/x/":         false,
		"media: /mnt/../etc":     false,
		"Media: /mnt/x":          false,
	} {
		dir := t.TempDir()
		y := strings.Replace(skeleton["fleet.yaml"], "spec:\n", "spec:\n  paths: {"+raw+"}\n", 1)
		if err := os.WriteFile(filepath.Join(dir, "fleet.yaml"), []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(dir)
		if (err == nil) != ok {
			t.Errorf("%s: err = %v", raw, err)
		}
	}
}

func TestMirrors(t *testing.T) {
	for raw, ok := range map[string]bool{
		`["git@github.com:me/fleet-mirror.git"]`: true,
		`["ssh://u@storagebox:23/./fleet.git"]`:  true,
		`["--upload-pack=x"]`:                    false,
		`[""]`:                                   false,
		`["a b"]`:                                false,
	} {
		dir := t.TempDir()
		y := strings.Replace(skeleton["fleet.yaml"], "spec:\n", "spec:\n  mirrors: "+raw+"\n", 1)
		if err := os.WriteFile(filepath.Join(dir, "fleet.yaml"), []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); (err == nil) != ok {
			t.Errorf("%s: err = %v", raw, err)
		}
	}
}

func TestKumaRollingUpdates(t *testing.T) {
	for raw, want := range map[string]string{
		"":                              "incident",
		", rollingUpdates: maintenance": "maintenance",
		", rollingUpdates: none":        "none",
		", rollingUpdates: sometimes":   "",
	} {
		dir := t.TempDir()
		y := strings.Replace(skeleton["fleet.yaml"], "spec:\n", "spec:\n  uptimeKuma: [{name: k, url: \"https://k\""+raw+"}]\n", 1)
		if err := os.WriteFile(filepath.Join(dir, "fleet.yaml"), []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := Load(dir)
		switch {
		case want == "" && err == nil:
			t.Errorf("%q: accepted", raw)
		case want != "" && (err != nil || f.Spec.UptimeKuma[0].RollingUpdates != want):
			t.Errorf("%q: %v %v", raw, f, err)
		}
	}
}
