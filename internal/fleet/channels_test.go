// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestChannels(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if err := SetChannel(dir, "phone", "NOTIFY_PHONE", "warning"); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := f.Spec.Notifications
	if n.Channels["phone"].URLSecret != "NOTIFY_PHONE" || len(n.Routes) != 1 || !slices.Equal(n.Routes[0].Channels, []string{"phone"}) || n.Routes[0].MinSeverity != "warning" {
		t.Fatalf("after adding: %+v", n)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "fleet.yaml"))
	if !strings.Contains(string(raw), "# Secrets are committed encrypted") {
		t.Error("the comments went")
	}

	// Again (a new URL): the channel is replaced, no second route.
	if err := SetChannel(dir, "phone", "NOTIFY_PHONE", "error"); err != nil {
		t.Fatal(err)
	}
	// A second channel, then a route shared by both.
	if err := SetChannel(dir, "mail", "NOTIFY_MAIL", ""); err != nil {
		t.Fatal(err)
	}
	f, _ = Load(dir)
	if len(f.Spec.Notifications.Routes) != 2 {
		t.Errorf("routes = %+v", f.Spec.Notifications.Routes)
	}

	// Removing takes it out of its routes and drops the emptied route.
	if found, err := RemoveChannel(dir, "phone"); err != nil || !found {
		t.Fatalf("remove: %v %v", found, err)
	}
	f, _ = Load(dir)
	n = f.Spec.Notifications
	if _, ok := n.Channels["phone"]; ok || len(n.Routes) != 1 || !slices.Equal(n.Routes[0].Channels, []string{"mail"}) {
		t.Errorf("after removing: %+v", n)
	}
	if found, err := RemoveChannel(dir, "phone"); err != nil || found {
		t.Errorf("remove again: %v %v", found, err)
	}
}

func TestSetRoutes(t *testing.T) {
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []string{"phone", "mail"} {
		if err := SetChannel(dir, ch, "NOTIFY_"+strings.ToUpper(ch), ""); err != nil {
			t.Fatal(err)
		}
	}
	want := []NotifyRoute{
		{Channels: []string{"phone"}, Events: []string{"apply.*"}, MinSeverity: "error"},
		{Channels: []string{"mail", "phone"}, Stacks: []string{"web"}},
	}
	if err := SetRoutes(dir, want); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := f.Spec.Notifications.Routes
	if len(got) != 2 || !slices.Equal(got[0].Events, []string{"apply.*"}) || got[0].MinSeverity != "error" || !slices.Equal(got[1].Stacks, []string{"web"}) {
		t.Fatalf("routes: %+v", got)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "fleet.yaml"))
	// An unknown channel is refused, and fleet.yaml stays as it was.
	if err := SetRoutes(dir, []NotifyRoute{{Channels: []string{"pager"}}}); err == nil {
		t.Error("a route to an unknown channel was written")
	}
	if now, _ := os.ReadFile(filepath.Join(dir, "fleet.yaml")); string(now) != string(raw) {
		t.Error("fleet.yaml changed after a refused edit")
	}
	if err := SetRoutes(dir, []NotifyRoute{}); err != nil {
		t.Errorf("no routes: %v", err)
	}
}
