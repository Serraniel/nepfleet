// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

func TestNamedPaths(t *testing.T) {
	f := &fleet.Fleet{}
	f.Spec.Paths = map[string]string{"media": "/mnt/TitanMedia"}
	if p, err := f.ResolvePath("@media"); err != nil || p != "/mnt/TitanMedia" {
		t.Errorf("resolve: %q %v", p, err)
	}
	if p, _ := f.ResolvePath("/srv/x"); p != "/srv/x" {
		t.Errorf("plain paths pass through: %q", p)
	}
	if _, err := f.ResolvePath("@nope"); err == nil {
		t.Error("an unknown name must be an error")
	}
	inp := template.Input{Name: "MEDIA", Type: "path"}
	if err := CheckInput(inp, "@media"); err != nil {
		t.Errorf("@name must be accepted: %v", err)
	}
	if err := CheckInput(inp, "@../etc"); err == nil {
		t.Error("@ with a non-name must be refused")
	}
}

func TestRenderResolvesNamedPaths(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.yaml", "services:\n  app:\n    image: nginx:1.29\n    volumes: [\"${MEDIA}:/media\"]\n    environment: {HANDLE: \"${HANDLE}\"}\n")
	tpl := &template.Template{Dir: dir, Spec: template.Spec{Compose: "compose.yaml", Inputs: []template.Input{
		{Name: "MEDIA", Type: "path"}, {Name: "HANDLE", Type: "string"}, {Name: "CACHE", Type: "path", Default: "@cache"},
	}}}
	f := &fleet.Fleet{}
	f.Spec.Paths = map[string]string{"media": "/mnt/TitanMedia", "cache": "/srv/cache"}
	s := &fleet.Stack{}
	s.Metadata.Name = "jf"
	s.Spec.Values = fleet.Literals(map[string]string{"MEDIA": "@media", "HANDLE": "@me@social.example"})
	out, err := Render(Input{Fleet: f, Stack: s, Template: tpl})
	if err != nil {
		t.Fatal(err)
	}
	env := EnvValues(out.Files[EnvFile])
	if env["MEDIA"] != "/mnt/TitanMedia" || env["CACHE"] != "/srv/cache" || env["HANDLE"] != "@me@social.example" {
		t.Errorf("env = %v", env)
	}
	s.Spec.Values["MEDIA"] = fleet.Lit("@nope")
	if _, err := Render(Input{Fleet: f, Stack: s, Template: tpl}); err == nil {
		t.Error("an undefined name must fail the render")
	}
}
