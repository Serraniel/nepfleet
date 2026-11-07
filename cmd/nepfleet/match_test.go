// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestMatchImages(t *testing.T) {
	stack := imageTags([]byte("services:\n  web: {image: \"ghcr.io/paperless-ngx/paperless-ngx:2.18\"}\n  db: {image: postgres:16}\n  x: {image: \"${IMG}\"}\n"))
	if len(stack) != 2 || stack["postgres"] != "16" {
		t.Fatalf("imageTags = %v", stack)
	}
	tpl := imageTags([]byte("services:\n  web: {image: \"ghcr.io/paperless-ngx/paperless-ngx:3.4\"}\n  db: {image: postgres:18-alpine}\n  broker: {image: valkey/valkey:9}\n"))
	m, ok := matchImages(stack, tpl)
	if !ok || len(m.Same) != 2 || m.Same["postgres"] != [2]string{"16", "18-alpine"} || !slices.Equal(m.OnlyTemplate, []string{"valkey/valkey"}) {
		t.Errorf("match = %+v, %v", m, ok)
	}
	if _, ok := matchImages(imageTags([]byte("services: {pg: {image: postgres:18}}")), tpl); ok {
		t.Error("a shared database alone is not a match")
	}
}

func TestDiffCompose(t *testing.T) {
	stack := []byte(`services:
  app:
    image: ghcr.io/example/app:1
    environment: ["API_KEY=topsecret", "OLD=1", "SHARED=1"]
    ports: ["8080:80"]
    volumes: ["/srv/app:/data", "/srv/same:/same"]
  mine:
    image: example/other:1
`)
	tpl := []byte(`services:
  web:
    image: ghcr.io/example/app:2
    environment: {NEW: "x", SHARED: "y"}
    volumes:
      - data:/data
      - /srv/same:/same
`)
	merged := composeServices([]byte("x-env: &e {FROM_ANCHOR: 1}\nservices:\n  a:\n    image: example/a:1\n    environment:\n      <<: *e\n      OWN: 2\n"))
	if len(merged) != 1 || !merged[0].Env["FROM_ANCHOR"] || !merged[0].Env["OWN"] || merged[0].Env["<<"] {
		t.Errorf("merge keys: %+v", merged)
	}
	var b bytes.Buffer
	diffCompose(&b, stack, tpl)
	got := b.String()
	for _, want := range []string{
		"service app (template: web)",
		"environment, yours only: API_KEY, OLD",
		"environment, template only: NEW",
		"ports, yours only: 8080:80",
		"volumes, yours only: /srv/app:/data",
		"volumes, template only: data:/data",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "topsecret") || strings.Contains(got, "mine") || strings.Contains(got, "/srv/same") {
		t.Errorf("values or unpaired services shown:\n%s", got)
	}
}
