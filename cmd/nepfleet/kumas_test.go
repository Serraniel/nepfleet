// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

// An Uptime Kuma stack counts as the fleet's Kuma, without fleet.yaml naming it.
func TestKumasFromStacks(t *testing.T) {
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
	stack("status", "  target: local\n  template: {source: main, name: uptime-kuma}\n  values:\n    KUMA_USERNAME: ops\n")
	stack("elsewhere", "  target: other\n  template: {source: main, name: uptime-kuma}\n")
	e, err := common{fleetDir: dir, stateDir: t.TempDir(), host: "h", offline: true}.open()
	if err != nil {
		t.Fatal(err)
	}
	kumas, err := e.kumas()
	if err != nil || len(kumas) != 1 {
		t.Fatalf("%+v %v", kumas, err)
	}
	k := kumas[0]
	if k.Name != "status" || k.URL != "http://status-uptime-kuma-1:3001" || k.Username != "ops" || k.RollingUpdates != "incident" {
		t.Errorf("%+v", k.KumaInstance)
	}
}
