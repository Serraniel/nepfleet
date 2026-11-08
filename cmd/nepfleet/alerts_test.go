// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/notify"
)

func TestAlertStack(t *testing.T) {
	stacks := []string{"web", "web-shop", "db"}
	for container, want := range map[string]string{
		"web-app-1":         "web",
		"web-shop-worker-1": "web-shop",
		"db-postgres-1":     "db",
		"other-x-1":         "",
		"web":               "",
		"":                  "",
	} {
		if got := alertStack(container, stacks); got != want {
			t.Errorf("%q: %q, want %q", container, got, want)
		}
	}
}

func TestAlertEvent(t *testing.T) {
	ev := alertEvent(api.Alert{Source: "Dozzle!", Rule: "panic", Container: "web-app-1", Host: "nas", Detail: "panic: boom"}, []string{"web"})
	if ev.Name != "alert.dozzle" || ev.Severity != notify.Warning || ev.Stack != "web" || ev.Title != "panic in web-app-1" ||
		!strings.HasPrefix(ev.Message, "panic: boom") || !strings.Contains(ev.Message, "dozzle on nas") {
		t.Errorf("%+v", ev)
	}
	// The line varies, the event for dedup doesn't.
	other := alertEvent(api.Alert{Source: "dozzle", Rule: "panic", Container: "web-app-1", Detail: "panic: other"}, []string{"web"})
	if other.Name != ev.Name || other.Title != ev.Title || other.Stack != ev.Stack {
		t.Errorf("same rule, same container: %+v", other)
	}
	if e := alertEvent(api.Alert{Detail: "x"}, nil); e.Name != "alert.external" || e.Title != "alert" || e.Stack != "" {
		t.Errorf("bare: %+v", e)
	}
}
