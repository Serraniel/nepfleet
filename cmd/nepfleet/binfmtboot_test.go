// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/doctor"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

func TestReregisterBinfmt(t *testing.T) {
	dir := t.TempDir()
	if err := fleet.Init(dir); err != nil {
		t.Fatal(err)
	}
	write := func(name, target, arches string) {
		st := filepath.Join(dir, "stacks", name)
		if err := os.MkdirAll(st, 0o750); err != nil {
			t.Fatal(err)
		}
		y := "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: " + name + "}\nspec:\n  target: " + target +
			"\n  template: {source: official, name: binfmt}\n"
		if arches != "" {
			y += "  values: {ARCHES: \"" + arches + "\"}\n"
		}
		if err := os.WriteFile(filepath.Join(st, "stack.yaml"), []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("binfmt", "local", "arm64")
	write("emu-other", "elsewhere", "")
	f, err := fleet.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	env := doctor.Env{BinfmtDir: "/b", ReadFile: func(p string) ([]byte, error) {
		switch {
		case p == "/b/status":
			return []byte("enabled\n"), nil
		case registered[strings.TrimPrefix(p, "/b/")]:
			return []byte("enabled\ninterpreter /usr/bin/qemu\n"), nil
		}
		return nil, os.ErrNotExist
	}}
	var started []string
	start := func(_ context.Context, project string) error {
		started = append(started, project)
		registered["qemu-aarch64"] = true
		return nil
	}
	var log bytes.Buffer
	reregisterBinfmt(context.Background(), f, "this", env, start, &log)
	if strings.Join(started, ",") != "binfmt" || !strings.Contains(log.String(), "registered emulators for arm64 again") {
		t.Fatalf("after a reboot: started %v, log %q", started, log.String())
	}
	// Registered: nothing to do.
	started = nil
	reregisterBinfmt(context.Background(), f, "this", env, start, &log)
	if len(started) != 0 {
		t.Errorf("started %v while the emulator is there", started)
	}
}
