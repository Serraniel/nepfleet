// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

func TestUnitName(t *testing.T) {
	for path, want := range map[string]string{
		"/mnt/share":       `mnt-share.mount`,
		"/mnt/Titan-Media": `mnt-Titan\x2dMedia.mount`,
		"/srv/a.b":         `srv-a.b.mount`,
	} {
		if got := UnitName(path, "mount"); got != want {
			t.Errorf("%s: %s", path, got)
		}
	}
}

func TestMountSteps(t *testing.T) {
	mounts := []fleet.HostMount{
		{Name: "box", Type: "cifs", Source: "//u1.example/backup", Target: "/mnt/share", User: "u1", PasswordSecret: "BOX"},
		{Name: "nas", Type: "nfs", Source: "nas.local:/media", Target: "/mnt/media", Options: []string{"ro"}},
		{Name: "far", Type: "sshfs", Source: "me@far.example:/data", Target: "/mnt/far"},
	}
	steps, err := MountSteps("debian", mounts, map[string]string{"box": "s3cret'x"}, []string{"old /mnt/old", "box /mnt/share", "bad; /etc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 5 {
		t.Fatalf("%s", texts(steps))
	}
	if !strings.Contains(steps[0].Script, "nfs-common") || !strings.Contains(steps[0].Script, "cifs-utils") || !strings.Contains(steps[0].Script, "sshfs") {
		t.Errorf("packages: %s", steps[0].Script)
	}
	box := steps[1].Script
	for _, want := range []string{"password=s3cret'x", "chmod 600 '/etc/nepfleet/mounts/box.cred'", "Options=_netdev,nofail,vers=3.1.1,seal,credentials=", "enable --now 'mnt-share.automount'"} {
		if !strings.Contains(box, want) {
			t.Errorf("missing %q in\n%s", want, box)
		}
	}
	// The password isn't in the world-readable unit, or in what's shown.
	unit := MountUnit(mounts[0])
	if strings.Contains(unit, "s3cret") {
		t.Errorf("password in the unit: %s", unit)
	}
	if strings.Contains(texts(steps), "s3cret") {
		t.Errorf("password in the plan: %s", texts(steps))
	}
	if !strings.Contains(MountUnit(mounts[1]), "vers=4.2,hard,ro") {
		t.Errorf("nfs: %s", MountUnit(mounts[1]))
	}
	if !strings.Contains(MountUnit(mounts[2]), "Type=fuse.sshfs") {
		t.Errorf("sshfs: %s", MountUnit(mounts[2]))
	}
	// Only what's managed and unlisted goes, and a bad line is ignored.
	last := steps[4]
	if !strings.Contains(last.Text, "/mnt/old") || !strings.Contains(last.Text, "data stays") || strings.Contains(last.Script, "rm -rf") {
		t.Errorf("removal: %+v", last)
	}
	if _, err := MountSteps("debian", mounts[:1], map[string]string{"box": "a\nb"}, nil); err == nil {
		t.Error("a line break in a password must be refused")
	}
	if _, err := MountSteps("plan9", mounts, nil, nil); err == nil {
		t.Error("an unknown distribution must be refused")
	}
}
