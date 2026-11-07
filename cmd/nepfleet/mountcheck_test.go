// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"slices"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

func TestBindSources(t *testing.T) {
	compose := []byte("services:\n  a:\n    volumes:\n      - ${MEDIA}/films:/films:ro\n      - data:/data\n      - ./local:/local\n      - {type: bind, source: /mnt/long/x, target: /x}\n      - {type: volume, source: named, target: /y}\n")
	got := bindSources(compose, map[string]string{"MEDIA": "/mnt/media"})
	slices.Sort(got)
	if !slices.Equal(got, []string{"/mnt/long/x", "/mnt/media/films"}) {
		t.Errorf("%v", got)
	}
}

func TestMountsNeeded(t *testing.T) {
	mounts := []fleet.HostMount{{Name: "a", Target: "/mnt/media"}, {Name: "b", Target: "/mnt/other"}}
	got := mountsNeeded(mounts, []string{"/mnt/media/films", "/mnt/mediaplus/x"})
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("%+v", got)
	}
}

func TestIsMounted(t *testing.T) {
	info := `36 35 98:0 /mnt1 /mnt/real rw,noatime master:1 - nfs4 nas:/x rw
37 35 0:30 / /mnt/auto rw,relatime master:2 - autofs systemd-1 rw
38 35 0:31 / /mnt/both rw - autofs systemd-1 rw
39 38 98:1 / /mnt/both rw - cifs //h/s rw
`
	for target, want := range map[string]bool{"/mnt/real": true, "/mnt/auto": false, "/mnt/both": true, "/mnt/none": false} {
		if got := isMounted(info, target); got != want {
			t.Errorf("%s: %v", target, got)
		}
	}
}
