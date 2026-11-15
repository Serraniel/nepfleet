// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

func TestParseComposePS(t *testing.T) {
	lines := `{"ID":"abc","Name":"app-web-1","Service":"web","State":"running","Health":"healthy","Status":"Up 2 hours (healthy)","Image":"nginx:1.29"}
{"ID":"def","Name":"app-db-1","Service":"db","State":"exited","Health":"","Status":"Exited (1) 3 minutes ago","Image":"postgres:18"}`
	rows, err := parseComposePS(lines)
	if err != nil || len(rows) != 2 || rows[0].Service != "web" || rows[1].State != "exited" {
		t.Fatalf("lines: %+v %v", rows, err)
	}
	array := `[{"ID":"abc","Name":"app-web-1","Service":"web","State":"running"}]`
	if rows, err := parseComposePS(array); err != nil || len(rows) != 1 || rows[0].Name != "app-web-1" {
		t.Errorf("array: %+v %v", rows, err)
	}
	if rows, err := parseComposePS("  \n"); err != nil || rows != nil {
		t.Errorf("empty: %+v %v", rows, err)
	}
	if _, err := parseComposePS("{nope"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestParseStackPS(t *testing.T) {
	raw := `{"Name":"app_web.1","Image":"nginx:1.29@sha256:abc","Node":"vps","CurrentState":"Running 2 hours ago","Error":""}
{"Name":"app_api.2","Image":"api:1","Node":"nas","CurrentState":"Preparing 3 seconds ago","Error":"no suitable node"}`
	cs, err := parseStackPS("app", raw)
	if err != nil || len(cs) != 2 {
		t.Fatalf("%+v %v", cs, err)
	}
	if cs[0].Service != "web" || cs[0].Image != "nginx:1.29" || cs[0].State != "running" || cs[0].Node != "vps" {
		t.Errorf("web: %+v", cs[0])
	}
	if cs[1].Service != "api" || cs[1].State != "preparing" || cs[1].Error != "no suitable node" {
		t.Errorf("api: %+v", cs[1])
	}
}
