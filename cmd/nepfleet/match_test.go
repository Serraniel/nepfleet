// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"slices"
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
