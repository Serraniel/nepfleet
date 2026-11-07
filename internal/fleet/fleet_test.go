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
		"media: /mnt/TitanMedia": true,
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

func TestBlockedHosts(t *testing.T) {
	off, on := false, true
	f := &Fleet{}
	s := &Stack{}
	if got := BlockedHosts(f, s); !slices.Contains(got, "mobile.events.data.microsoft.com") {
		t.Errorf("telemetry is blocked by default: %v", got)
	}
	f.Spec.Egress = &Egress{BlockTelemetry: &off, Block: []string{"ads.example.org"}}
	if got := BlockedHosts(f, s); len(got) != 1 || got[0] != "ads.example.org" {
		t.Errorf("fleet off: %v", got)
	}
	s.Spec.Egress = &Egress{BlockTelemetry: &on, Block: []string{"ads.example.org"}}
	if got := BlockedHosts(f, s); !slices.Contains(got, "scarf.sh") || len(slices.Compact(slices.Clone(got))) != len(got) {
		t.Errorf("stack on, duplicates once: %v", got)
	}
	if checkEgress(&Egress{Block: []string{"*.posthog.com"}}) == nil {
		t.Error("wildcards cannot be mapped")
	}
}

func TestStorageValidation(t *testing.T) {
	for raw, ok := range map[string]bool{
		`{nas: {type: cifs, address: 192.168.1.10, share: media, username: u, passwordSecret: P}}`: true,
		`{box: {type: nfs, address: nfs.example.org, share: /exports}}`:                            true,
		`{x: {type: smb, address: a, share: s}}`:                                                   false,
		`{x: {type: nfs, address: a, share: exports}}`:                                             false,
		`{x: {type: cifs, address: "a b", share: s}}`:                                              false,
		`{x: {type: cifs, address: a, share: s, options: ["password=x"]}}`:                         false,
		`{x: {type: cifs, address: a, share: s, options: ["vers=3.0,sec=none"]}}`:                  false,
		`{x: {type: cifs, address: a, share: ../etc}}`:                                             false,
	} {
		dir := t.TempDir()
		y := strings.Replace(skeleton["fleet.yaml"], "spec:\n", "spec:\n  storage: "+raw+"\n", 1)
		if err := os.WriteFile(filepath.Join(dir, "fleet.yaml"), []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); (err == nil) != ok {
			t.Errorf("%s: err = %v", raw, err)
		}
	}
}

func TestHostsAndPorts(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fleet.yaml"), []byte(skeleton["fleet.yaml"]), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"hosts", "stacks/a", "stacks/b"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	must := func(path, content string) {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must("hosts/nas.yaml", "apiVersion: nepfleet/v1alpha1\nkind: Host\nmetadata: {name: nas}\nspec: {address: nas.local, ports: {from: 8060, to: 8062}, updates: {enabled: true, schedule: \"Sun 03:00\", reboot: when-required}}\n")
	stack := func(name, extra string) string {
		return "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: " + name + "}\nspec:\n  target: nas\n" + extra
	}
	must("stacks/a/stack.yaml", stack("a", "  access: {mode: port, port: 8060}\n"))
	must("stacks/b/stack.yaml", stack("b", ""))
	must("stacks/a/compose.yaml", "services: {}\n")
	must("stacks/b/compose.yaml", "services: {}\n")
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.LoadHost("nas")
	if err != nil || h.Spec.Address != "nas.local" || h.PortRange().From != 8060 {
		t.Fatalf("host: %+v %v", h, err)
	}
	if p, err := f.FreePort("nas"); err != nil || p != 8061 {
		t.Errorf("free port = %d, %v", p, err)
	}
	if h, err := f.LoadHost("other"); err != nil || h.Spec.Address != "other" || h.PortRange() != DefaultPorts {
		t.Errorf("a host without a file: %+v %v", h, err)
	}
	if _, err := f.LoadHost("../x"); err == nil {
		t.Error("a host name with a path must be refused")
	}
	must("stacks/b/stack.yaml", stack("b", "  access: {mode: port, port: 80}\n"))
	if _, err := f.LoadStack("b"); err == nil {
		t.Error("a port below 1024 must be refused")
	}
}

func TestHostMountRules(t *testing.T) {
	good := HostMount{Name: "box", Type: "cifs", Source: "//u1.example/backup", Target: "/mnt/share", User: "u1", PasswordSecret: "BOX"}
	if err := good.validate(); err != nil {
		t.Errorf("good: %v", err)
	}
	if err := (HostMount{Name: "n", Type: "nfs", Source: "nas.local:/media", Target: "/mnt/media", Options: []string{"ro", "rsize=1048576"}}).validate(); err != nil {
		t.Errorf("nfs: %v", err)
	}
	for name, change := range map[string]func(*HostMount){
		"type":            func(m *HostMount) { m.Type = "ext4" },
		"name":            func(m *HostMount) { m.Name = "a/b" },
		"source shell":    func(m *HostMount) { m.Source = "//h/s; reboot" },
		"source newline":  func(m *HostMount) { m.Source = "//h/s\nExecStart=/bin/x" },
		"target relative": func(m *HostMount) { m.Target = "mnt/x" },
		"target root":     func(m *HostMount) { m.Target = "/" },
		"target system":   func(m *HostMount) { m.Target = "/etc/nepfleet" },
		"target var":      func(m *HostMount) { m.Target = "/var/lib/docker" },
		"target dots":     func(m *HostMount) { m.Target = "/mnt/../etc" },
		"target quote":    func(m *HostMount) { m.Target = "/mnt/a'b" },
		"option":          func(m *HostMount) { m.Options = []string{"x,exec=/bin/sh"} },
		"no password":     func(m *HostMount) { m.PasswordSecret = "" },
		"user":            func(m *HostMount) { m.User = "a b" },
	} {
		m := good
		change(&m)
		if err := m.validate(); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}
