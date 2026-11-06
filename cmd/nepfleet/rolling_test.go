// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import "testing"

func TestRollingUpdate(t *testing.T) {
	for compose, want := range map[string]bool{
		"services:\n  web:\n    labels: {kuma.web.http.name: Web}\n    deploy: {replicas: 3}\n":                                             true,
		"services:\n  web:\n    deploy: {replicas: 2, labels: [\"kuma.web.http.name=Web\"]}\n":                                              true,
		"services:\n  web:\n    labels: {kuma.web.http.name: Web}\n    deploy: {mode: global}\n":                                            true,
		"services:\n  web:\n    labels: {kuma.web.http.name: Web}\n    deploy: {replicas: 1}\n":                                             false,
		"services:\n  web:\n    labels: {kuma.web.http.name: Web}\n":                                                                        false,
		"services:\n  web:\n    labels: {kuma.web.http.name: Web}\n    deploy: {replicas: 2}\n  db:\n    labels: {kuma.db.port.name: DB}\n": false,
		"services:\n  web:\n    labels: {kuma.web.http.name: Web}\n    deploy: {replicas: 2}\n  worker:\n    image: w\n":                    true,
		"services:\n  worker:\n    deploy: {replicas: 3}\n":                                                                                 false,
		"x: [}": false,
	} {
		if got := rollingUpdate([]byte(compose)); got != want {
			t.Errorf("%q: rolling = %v, want %v", compose, got, want)
		}
	}
}
