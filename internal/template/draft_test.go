// SPDX-License-Identifier: AGPL-3.0-or-later

package template

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const genCompose = `version: "3.8"
name: myapp
services:
  app:
    image: ghcr.io/acme/app:1.2.3
    container_name: app
    ports:
      - "8080:8080"
      - "127.0.0.1:9100:9100"
    environment:
      DB_PASSWORD: hunter2-real-secret
      API_KEY: real-api-key-value
      LOG_LEVEL: ${LOG_LEVEL:-info}
      PUBLIC_URL: ${PUBLIC_URL}
      APP_PORT: ${APP_PORT:-8080}
      DEBUG: ${DEBUG:-false}
      TZ: ${TZ}
      SESSION_SECRET: ${SESSION_SECRET:-a-default-secret}
      DOMAIN: app.example.org
    volumes:
      - app_data:/data
      - /srv/media:/media
      - /var/run/docker.sock:/var/run/docker.sock
  db:
    image: postgres:17
    ports: ["5432:5432"]
    environment:
      - POSTGRES_PASSWORD=another-real-secret
      - POSTGRES_DB=app
    volumes:
      - type: volume
        source: db_data
        target: /var/lib/postgresql/data
  latest:
    image: acme/tool:2
    privileged: true
volumes:
  app_data:
  db_data:
`

func draftDir(t *testing.T, d *Draft, name string) *Template {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(d.Template)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "template.yaml"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), d.Compose, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v\n%s", err, raw)
	}
	return loaded
}

func TestGenerateMakesAValidTemplate(t *testing.T) {
	d, err := DraftFrom([]byte(genCompose), "myapp")
	if err != nil {
		t.Fatal(err)
	}
	tpl := draftDir(t, d, "myapp")
	if err := tpl.Validate(); err != nil {
		t.Fatalf("the draft must validate: %v\n%s", err, d.Compose)
	}
	spec := tpl.Spec
	all := string(d.Compose)
	for _, secret := range []string{"hunter2-real-secret", "real-api-key-value", "another-real-secret", "a-default-secret"} {
		if strings.Contains(all, secret) {
			t.Errorf("%s reached the draft:\n%s", secret, all)
		}
	}
	generated := func(n string) bool {
		return slices.ContainsFunc(spec.Generate, func(g Generate) bool { return g.Name == n })
	}
	input := func(n string) *Input {
		for i := range spec.Inputs {
			if spec.Inputs[i].Name == n {
				return &spec.Inputs[i]
			}
		}
		return nil
	}
	for _, n := range []string{"DB_PASSWORD", "POSTGRES_PASSWORD", "SESSION_SECRET"} {
		if !generated(n) {
			t.Errorf("%s should be generated: %+v", n, spec.Generate)
		}
	}
	if in := input("API_KEY"); in == nil || in.Type != "secret" || !in.Required {
		t.Errorf("API_KEY comes from elsewhere: %+v", in)
	}
	for name, want := range map[string]Input{
		"LOG_LEVEL":  {Type: "string", Default: "info"},
		"PUBLIC_URL": {Type: "url", Required: true},
		"APP_PORT":   {Type: "int", Default: "8080"},
		"DEBUG":      {Type: "bool", Default: "false"},
		"DOMAIN":     {Type: "hostname", Required: true},
	} {
		got := input(name)
		if got == nil || got.Type != want.Type || got.Default != want.Default || got.Required != want.Required {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if input("TZ") != nil {
		t.Error("a builtin isn't an input")
	}
	if spec.Web == nil || spec.Web.Service != "app" || spec.Web.Port != 8080 || !spec.Supports.Ingress {
		t.Errorf("web: %+v", spec.Web)
	}
	if !strings.Contains(all, "caddy: \"${DOMAIN}\"") || !strings.Contains(all, "{{upstreams 8080}}") {
		t.Errorf("no ingress:\n%s", all)
	}
	if strings.Contains(all, "8080:8080") || !strings.Contains(all, "9100:9100") {
		t.Errorf("only the web port moves:\n%s", all)
	}
	if strings.Contains(all, "container_name") || strings.Contains(all, "version:") || strings.Contains(all, "name: myapp") {
		t.Errorf("not stripped:\n%s", all)
	}
	if !strings.Contains(all, "app-data:/data") || !strings.Contains(all, "source: db-data") {
		t.Errorf("volumes renamed everywhere:\n%s", all)
	}
	if len(spec.Volumes) != 2 || spec.Volumes[0].Name != "app-data" || !spec.Volumes[0].Backup {
		t.Errorf("%+v", spec.Volumes)
	}
	names := []string{}
	for _, i := range spec.Images {
		names = append(names, i.Name)
	}
	if !slices.Equal(names, []string{"ghcr.io/acme/app", "postgres", "acme/tool"}) {
		t.Errorf("%v", names)
	}
	notes := strings.Join(d.Notes, "\n")
	for _, want := range []string{"Docker socket", "/srv/media", "privileged", "still publishes", "DB_PASSWORD looked like a secret"} {
		if !strings.Contains(notes, want) {
			t.Errorf("no note about %q:\n%s", want, notes)
		}
	}
	for _, secret := range []string{"hunter2", "real-api-key", "a-default-secret"} {
		if strings.Contains(notes, secret) {
			t.Errorf("a note holds a secret: %s", notes)
		}
	}
}

// The validator refuses an image with no tag, so the draft says what to set.
func TestGenerateNotesMissingTags(t *testing.T) {
	d, err := DraftFrom([]byte("services:\n  a:\n    image: acme/tool\n  b:\n    image: acme/other:latest\n"), "x")
	if err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(d.Notes, "\n")
	if !strings.Contains(notes, "acme/tool has no fixed tag") || !strings.Contains(notes, "acme/other:latest has no fixed tag") {
		t.Errorf("%s", notes)
	}
}

func TestGenerateRefuses(t *testing.T) {
	for name, compose := range map[string]string{
		"builds":     "services:\n  a:\n    build: .\n",
		"no image":   "services:\n  a:\n    command: x\n",
		"empty":      "",
		"no service": "volumes:\n  a:\n",
		"not yaml":   "services: [",
		"lowercase":  "services:\n  a:\n    image: x:1\n    command: ${oops}\n",
		"only vars":  "services:\n  a:\n    image: ${IMAGE}\n",
	} {
		if _, err := DraftFrom([]byte(compose), "x"); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if _, err := DraftFrom([]byte("services:\n  a:\n    image: x:1\n"), "Bad Name"); err == nil {
		t.Error("a bad name must be refused")
	}
}

func TestGenerateWithoutWebUI(t *testing.T) {
	d, err := DraftFrom([]byte("services:\n  worker:\n    image: acme/worker:1\n  db:\n    image: postgres:17\n    ports: [\"5432:5432\"]\n"), "x")
	if err != nil {
		t.Fatal(err)
	}
	if d.Template.Spec.Web != nil {
		t.Errorf("a database isn't a web UI: %+v", d.Template.Spec.Web)
	}
	if err := draftDir(t, d, "x").Validate(); err != nil {
		t.Errorf("%v\n%s", err, d.Compose)
	}
}
