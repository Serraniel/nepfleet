// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

// Render end to end with untrusted values: a template plus whatever a user
// (or an imported stack) puts in stack.yaml and the secrets file. It must
// not panic, and what it accepts must reach the env file unchanged and
// leave a compose file that still parses.
func FuzzRender(f *testing.F) {
	f.Add("app.example.org", "@media", "/dev/serial/by-id/usb-x", "intel", "s3cr$t", "8080", "a")
	f.Add("", "./data", "", "nvidia", "", "x", "")
	f.Add("a.example", "./x@media", "", "none", "", "80", "a")
	f.Add("x'y", "@nope", "/dev/../etc", "none", "ends\\", "-1", "b\nc")
	dir := f.TempDir()
	compose := "services:\n  app:\n    image: nginx:1.29\n    ports: [\"${PORT}:80\"]\n    volumes: [\"${DATA}:/data\"]\n    environment: {D: \"${DOMAIN}\", P: \"${PASS}\", M: \"${MODE}\"}\n    labels: {caddy: \"${DOMAIN}\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0o600); err != nil {
		f.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conf.txt"), []byte("domain ${DOMAIN} $${LITERAL}\n"), 0o600); err != nil {
		f.Fatal(err)
	}
	tpl := &template.Template{Dir: dir, Spec: template.Spec{
		Compose: "compose.yaml",
		Inputs: []template.Input{
			{Name: "DOMAIN", Type: "hostname", Required: true},
			{Name: "DATA", Type: "path", Default: "./data"},
			{Name: "DEV", Type: "device"},
			{Name: "GPU", Type: "gpu", Default: "none"},
			{Name: "PASS", Type: "secret"},
			{Name: "PORT", Type: "int", Default: "8080"},
			{Name: "MODE", Type: "enum", Options: []string{"a", "b"}, Default: "a"},
		},
		Files:    []template.File{{Src: "conf.txt", Dest: "conf/app.txt"}},
		Hardware: []template.Hardware{{Input: "DEV", Services: []string{"app"}}, {Input: "GPU", Services: []string{"app"}}},
	}}
	fl := &fleet.Fleet{}
	fl.Spec.Paths = map[string]string{"media": "/mnt/media"}
	fl.Spec.IngressNetwork = "ingress"
	f.Fuzz(func(t *testing.T, domain, data, dev, gpu, pass, port, mode string) {
		st := &fleet.Stack{}
		st.Metadata.Name = "app"
		st.Spec.Values = fleet.Literals(map[string]string{"DOMAIN": domain, "DATA": data, "DEV": dev, "GPU": gpu, "PORT": port, "MODE": mode})
		out, err := Render(Input{Fleet: fl, Stack: st, Template: tpl, Secrets: map[string]string{"PASS": pass}, RenderGID: "989"})
		if err != nil {
			return
		}
		env := EnvValues(out.Files[EnvFile])
		// An empty optional value takes its default.
		if env["DOMAIN"] != domain || env["PASS"] != pass || (port != "" && env["PORT"] != port) {
			t.Fatalf("values changed on the way to the env file: %v", env)
		}
		// Only a whole @name resolves; "./x@media" is an ordinary path.
		want := data
		if data == "@media" {
			want = "/mnt/media"
		}
		if data != "" && env["DATA"] != want {
			t.Fatalf("DATA %q rendered as %q", data, env["DATA"])
		}
		var doc map[string]any
		if err := yaml.Unmarshal(out.Files[ComposeFile], &doc); err != nil {
			t.Fatalf("rendered compose does not parse: %v", err)
		}
	})
}
