// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

func TestChannelsAPI(t *testing.T) {
	c, dir := secretFleet(t)
	const url = "ntfy://ntfy.example.org/secret-topic"
	commit, err := setChannelAPI(c, "phone", url, "warning", "ci")
	if err != nil {
		t.Fatal(err)
	}
	if head := strings.TrimSpace(tgit(t, dir, "rev-parse", "HEAD")); head != commit {
		t.Errorf("commit %s, HEAD %s", commit, head)
	}
	if log := tgit(t, dir, "log", "-1", "--format=%an %s"); !strings.Contains(log, "ci chore(fleet): set notification channel phone") {
		t.Errorf("commit: %s", log)
	}
	for _, f := range []string{"fleet.yaml", fleet.FleetSecretsFile} {
		raw, _ := os.ReadFile(filepath.Join(dir, f))
		if strings.Contains(string(raw), "secret-topic") {
			t.Fatalf("%s holds the URL in plain text", f)
		}
	}
	e, err := c.open()
	if err != nil {
		t.Fatal(err)
	}
	if ch := e.fleet.Spec.Notifications.Channels["phone"]; ch.URLSecret != "NOTIFY_PHONE" {
		t.Errorf("channel = %+v", ch)
	}
	if secrets, _ := e.store.Load(filepath.Join(dir, fleet.FleetSecretsFile)); secrets["NOTIFY_PHONE"] != url {
		t.Error("the URL isn't in the fleet's secrets")
	}

	if _, err := setChannelAPI(c, "bad", "nope://", "", "ci"); !errors.As(err, new(api.InvalidError)) {
		t.Errorf("an unknown service: %v", err)
	}

	if _, err := removeChannelAPI(c, "phone", "ci"); err != nil {
		t.Fatal(err)
	}
	e, _ = c.open()
	if _, ok := e.fleet.Spec.Notifications.Channels["phone"]; ok || len(e.fleet.Spec.Notifications.Routes) != 0 {
		t.Errorf("after removing: %+v", e.fleet.Spec.Notifications)
	}
	if secrets, _ := e.store.Load(filepath.Join(dir, fleet.FleetSecretsFile)); secrets["NOTIFY_PHONE"] != "" {
		t.Error("the URL stayed in the secrets")
	}
	if _, err := removeChannelAPI(c, "phone", "ci"); !errors.Is(err, api.ErrNoChannel) {
		t.Errorf("removing again: %v", err)
	}
	if st := tgit(t, dir, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("left uncommitted: %s", st)
	}
}
