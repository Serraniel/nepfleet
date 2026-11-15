// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

func TestInstalledTemplates(t *testing.T) {
	dir := t.TempDir()
	if err := fleet.Init(dir); err != nil {
		t.Fatal(err)
	}
	stack := func(name, spec string) {
		d := filepath.Join(dir, "stacks", name)
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
		y := "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: " + name + "}\nspec:\n" + spec
		if err := os.WriteFile(filepath.Join(d, "stack.yaml"), []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stack("caddy", "  target: h\n  template: {source: main, name: caddy}\n")
	stack("kuma", "  target: other\n  template: {source: main, name: uptime-kuma}\n")
	stack("home", "  target: local\n  template: {source: main, name: homepage}\n")
	stack("theirs", "  target: h\n  template: {source: extra, name: whoami}\n")
	e, err := common{fleetDir: dir, stateDir: t.TempDir(), host: "h", offline: true}.open()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(installedTemplates(e, "main"), ","); got != "caddy,homepage" {
		t.Errorf("main: %s", got)
	}
	if got := installedTemplates(e, "none"); got == nil || len(got) != 0 {
		t.Errorf("none: %#v", got)
	}
}
