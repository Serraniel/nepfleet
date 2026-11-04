// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

func TestHostPort(t *testing.T) {
	env := map[string]string{"HTTP": "80"}
	for in, want := range map[any]string{
		"80:80":             "80",
		"8080:80":           "8080",
		"80:8080":           "80",
		"127.0.0.1:443:443": "443",
		"443:443/udp":       "443",
		"${HTTP}:8080":      "80",
		"3000":              "",
	} {
		if got := hostPort(in, env); got != want {
			t.Errorf("hostPort(%v) = %q, want %q", in, got, want)
		}
	}
	if got := hostPort(map[string]any{"target": 80, "published": "443"}, env); got != "443" {
		t.Errorf("long syntax = %q", got)
	}
}
