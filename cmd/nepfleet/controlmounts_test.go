// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/remote"
)

func TestControlMounts(t *testing.T) {
	_, dir, _ := rollbackFleet(t)
	if err := os.MkdirAll(filepath.Join(dir, "hosts"), 0o750); err != nil {
		t.Fatal(err)
	}
	host := "apiVersion: nepfleet/v1alpha1\nkind: Host\nmetadata: {name: vps}\nspec:\n  address: vps\n  mounts:\n    - {name: media, type: nfs, source: \"nas:/media\", target: /mnt/media}\n"
	if err := os.WriteFile(filepath.Join(dir, "hosts", "vps.yaml"), []byte(host), 0o600); err != nil {
		t.Fatal(err)
	}
	var ran []string
	run := func(_ context.Context, script string) ([]byte, error) {
		ran = append(ran, script)
		return nil, nil // no mounts yet
	}
	rep := &remote.Report{OSID: "debian", Sudo: true}
	fleetDir := ""
	plan := controlMounts(func() string { return fleetDir })
	if _, err := plan(t.Context(), "vps", run, rep); err == nil || !strings.Contains(err.Error(), "settings") {
		t.Errorf("no folder chosen: %v", err)
	}
	fleetDir = dir
	if _, err := plan(t.Context(), "other", run, rep); err == nil || !strings.Contains(err.Error(), "hosts/other.yaml") {
		t.Errorf("no host file: %v", err)
	}
	steps, err := plan(t.Context(), "vps", run, rep)
	if err != nil || len(steps) == 0 || !strings.Contains(steps[len(steps)-1].Text, "/mnt/media") {
		t.Fatalf("steps: %+v %v", steps, err)
	}
	if len(ran) == 0 || ran[len(ran)-1] != remote.MountsPresent {
		t.Errorf("it didn't look at the machine's mounts first")
	}
}
