// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"strings"
	"testing"
)

func TestSetValues(t *testing.T) {
	raw := "apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: caddy}\nspec:\n  target: local\n  # the ingress\n  values:\n    CADDY_DNS_PROVIDER: cloudflare\n    DNS_RECORDS: \"off\"\n"
	out, err := setValues([]byte(raw), map[string]string{"DNS_RECORDS": "on", "DNS_ZONE": "example.net"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`DNS_RECORDS: "on"`, `DNS_ZONE: "example.net"`, "CADDY_DNS_PROVIDER: cloudflare", "# the ingress"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in:\n%s", want, s)
		}
	}
	if strings.Count(s, "DNS_RECORDS") != 1 {
		t.Errorf("DNS_RECORDS twice:\n%s", s)
	}
	// No values yet: the section is made.
	out, err = setValues([]byte("apiVersion: nepfleet/v1alpha1\nkind: Stack\nmetadata: {name: caddy}\nspec: {target: local}\n"), map[string]string{"DNS_RECORDS": "on"})
	if err != nil || !strings.Contains(string(out), `DNS_RECORDS: "on"`) {
		t.Errorf("new values: %s %v", out, err)
	}
}
