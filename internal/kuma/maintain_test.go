// SPDX-License-Identifier: AGPL-3.0-or-later

package kuma

import (
	"strings"
	"testing"
)

func TestMonitorNames(t *testing.T) {
	compose := []byte(`services:
  web:
    labels:
      caddy: x
      kuma.web.http.name: Web ${NEPFLEET_STACK}
      kuma.web.http.url: https://x
  db:
    labels: ["kuma.db.port.name=Database"]
  quiet:
    labels:
      kuma.q.http.name: Quiet
      nepfleet.maintenance: "false"
`)
	got := MonitorNames(compose, map[string]string{"NEPFLEET_STACK": "shop"})
	if strings.Join(got, ",") != "Database,Web shop" {
		t.Errorf("got %v", got)
	}
}
