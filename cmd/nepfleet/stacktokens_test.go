// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/render"
	"github.com/Serraniel/nepfleet/internal/template"
)

func TestStackTokens(t *testing.T) {
	e := &env{stateDir: t.TempDir()}
	tpl := &template.Template{}
	tpl.Spec.Generate = []template.Generate{{Name: "ALERTS", Kind: template.KindNotifyToken}, {Name: "KEY", Kind: "secret", Length: 32}}
	old := "nft_" + strings.Repeat("A", 43)
	sec := map[string]string{"ALERTS": old, "KEY": "k"}

	// No token store on this machine: nothing can be said, the token stays.
	if got := e.staleTokens(tpl, sec); got["ALERTS"] != old {
		t.Error("a token was dropped where the store can't be read")
	}

	// Registered: kept as long as it isn't about to expire.
	if err := e.registerTokens("dozzle", &render.Output{Tokens: map[string]string{"ALERTS": "notify"}, Generated: map[string]string{"ALERTS": old}}); err != nil {
		t.Fatal(err)
	}
	if got := e.staleTokens(tpl, sec); got["ALERTS"] != old {
		t.Error("a valid token was dropped")
	}
	tokens, _ := tokenStore(e.stateDir).List()
	if len(tokens) != 1 || tokens[0].Name != "stack:dozzle/ALERTS" || tokens[0].Scope != "notify" {
		t.Errorf("tokens = %+v", tokens)
	}

	// Revoked (or from another server): made again, without touching the caller's map.
	if err := tokenStore(e.stateDir).Revoke(tokens[0].ID); err != nil {
		t.Fatal(err)
	}
	got := e.staleTokens(tpl, sec)
	if _, ok := got["ALERTS"]; ok || got["KEY"] != "k" {
		t.Errorf("after revoking: %v", got)
	}
	if sec["ALERTS"] != old {
		t.Error("the caller's secrets were changed")
	}
}

func TestAPINetworkHelpers(t *testing.T) {
	if got := apiURLFor(":7480"); got != "http://nepfleet:7480" {
		t.Errorf("apiURLFor(:7480) = %q", got)
	}
	if got := apiURLFor("0.0.0.0:9000"); got != "http://nepfleet:9000" {
		t.Errorf("apiURLFor = %q", got)
	}
	if got := apiURLFor("nonsense"); got != render.DefaultAPIURL {
		t.Errorf("apiURLFor(nonsense) = %q", got)
	}
	id := strings.Repeat("ab", 32)
	line := "612 600 0:44 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw,relatime - ext4 /dev/vda1 rw\n"
	if m := containerIDRe.FindStringSubmatch(line); m == nil || m[1] != id {
		t.Errorf("container ID from mountinfo: %v", m)
	}
}
