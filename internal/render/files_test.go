// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

func TestStackFiles(t *testing.T) {
	tdir, sdir := t.TempDir(), t.TempDir()
	must(t, os.MkdirAll(filepath.Join(sdir, "files", "config"), 0o750))
	write(t, filepath.Join(tdir, "compose.yaml"), "services:\n  web:\n    image: nginx:1.29\n")
	write(t, filepath.Join(tdir, "default.yaml"), "theme: light\n")
	write(t, filepath.Join(tdir, "other.yaml"), "kept: true\n")
	tpl := &template.Template{Dir: tdir, Spec: template.Spec{Compose: "compose.yaml",
		Inputs: []template.Input{{Name: "TITLE", Type: "string"}, {Name: "API_KEY", Type: "secret"}},
		Files:  []template.File{{Src: "default.yaml", Dest: "config/settings.yaml"}, {Src: "other.yaml", Dest: "config/other.yaml"}}}}
	write(t, filepath.Join(sdir, "files", "config", "settings.yaml"), "title: ${TITLE}\nkey: ${API_KEY}\nliteral: $${HOME}\n")
	s := &fleet.Stack{Dir: sdir}
	s.Metadata.Name = "app"
	s.Spec.Target = "local"
	s.Spec.Values = fleet.Literals(map[string]string{"TITLE": "Home"})
	in := Input{Fleet: &fleet.Fleet{}, Stack: s, Template: tpl, Secrets: map[string]string{"API_KEY": "test-api-key-value"}}
	out, err := Render(in)
	must(t, err)
	if got := string(out.Files["config/settings.yaml"]); got != "title: Home\nkey: test-api-key-value\nliteral: ${HOME}\n" {
		t.Errorf("the stack's file replaces the template's: %q", got)
	}
	if string(out.Files["config/other.yaml"]) != "kept: true\n" {
		t.Errorf("other template files stay: %q", out.Files["config/other.yaml"])
	}
	if m := string(out.Masked("config/settings.yaml")); strings.Contains(m, "test-api-key") {
		t.Errorf("secrets must be masked in shown files: %q", m)
	}
	refused := func(what string) {
		t.Helper()
		if _, err := Render(in); err == nil {
			t.Error(what + " must be refused")
		}
	}
	outside := filepath.Join(tdir, "outside.txt")
	write(t, outside, "not part of the stack\n")
	link := filepath.Join(sdir, "files", "link")
	must(t, os.Symlink(outside, link))
	refused("a symlink")
	must(t, os.Remove(link))
	env := filepath.Join(sdir, "files", ".env")
	write(t, env, "X=1\n")
	refused(".env")
	must(t, os.Remove(env))
	write(t, filepath.Join(sdir, "files", "big.bin"), strings.Repeat("x", MaxStackFile+1))
	refused("a file over the size limit")
}
