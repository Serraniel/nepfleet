// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

func botCheckSetup(t *testing.T, compose string) (*template.Template, *fleet.Stack) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "compose.yaml"), compose)
	tpl := &template.Template{Dir: dir, Spec: template.Spec{Compose: "compose.yaml",
		Inputs: []template.Input{{Name: "DOMAIN", Type: "hostname", Required: true}},
		Web:    &template.Web{Service: "web", Port: 80}}}
	s := &fleet.Stack{}
	s.Metadata.Name = "app"
	s.Spec.Target = "local"
	s.Spec.Values = fleet.Literals(map[string]string{"DOMAIN": "app.example.org"})
	s.Spec.BotCheck = &fleet.BotCheck{Enabled: true, Allow: []string{"^/api/"}, Difficulty: 4}
	return tpl, s
}

const botCheckCompose = "services:\n  web:\n    image: nginx:1.29\n    networks: [ingress, default]\n    labels:\n      caddy: ${DOMAIN}\n      caddy.reverse_proxy: \"{{upstreams 80}}\"\n      caddy.request_body.max_size: 0\n      homepage.href: ${NEPFLEET_URL}\nnetworks:\n  ingress:\n    name: ${NEPFLEET_INGRESS_NETWORK}\n    external: true\n"

func TestBotCheck(t *testing.T) {
	tpl, s := botCheckSetup(t, botCheckCompose)
	out, err := Render(Input{Fleet: &fleet.Fleet{}, Stack: s, Template: tpl})
	must(t, err)
	var doc struct {
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Labels      map[string]string `yaml:"labels"`
			Environment map[string]string `yaml:"environment"`
			Networks    []string          `yaml:"networks"`
			Volumes     []string          `yaml:"volumes"`
		} `yaml:"services"`
	}
	must(t, yaml.Unmarshal(out.Files[ComposeFile], &doc))
	web, anubis := doc.Services["web"], doc.Services["web-anubis"]
	for k := range web.Labels {
		if isCaddyLabel(k) {
			t.Errorf("Caddy would still reach the service directly: %s", k)
		}
	}
	if web.Labels["homepage.href"] == "" {
		t.Error("other labels stay with the service")
	}
	want := map[string]string{
		"caddy":                         "${DOMAIN}",
		"caddy.reverse_proxy":           "{{upstreams 8923}}",
		"caddy.reverse_proxy.header_up": "X-Real-Ip {remote_host}",
		"caddy.request_body.max_size":   "0",
	}
	for k, v := range want {
		if anubis.Labels[k] != v {
			t.Errorf("anubis label %s = %q, want %q", k, anubis.Labels[k], v)
		}
	}
	if anubis.Environment["TARGET"] != "http://web:80" || !strings.HasPrefix(anubis.Image, "ghcr.io/techarohq/anubis:") || !strings.Contains(anubis.Image, "@sha256:") {
		t.Errorf("anubis: %+v", anubis)
	}
	if strings.Join(anubis.Networks, ",") != "ingress,default" {
		t.Errorf("networks: %v", anubis.Networks)
	}
	if len(anubis.Volumes) != 1 || !strings.HasPrefix(anubis.Volumes[0], "./"+AnubisPolicyFile+":") {
		t.Errorf("volumes: %v", anubis.Volumes)
	}
	policy := string(out.Files[AnubisPolicyFile])
	var parsed struct {
		Bots  []map[string]any `yaml:"bots"`
		DNSBL *bool            `yaml:"dnsbl"`
	}
	must(t, yaml.Unmarshal(out.Files[AnubisPolicyFile], &parsed))
	if len(parsed.Bots) != 3 || parsed.Bots[0]["path_regex"] != "^/api/" || parsed.Bots[0]["action"] != "ALLOW" ||
		parsed.Bots[2]["action"] != "CHALLENGE" || parsed.DNSBL == nil || *parsed.DNSBL {
		t.Errorf("policy:\n%s", policy)
	}
}

func TestBotCheckOff(t *testing.T) {
	tpl, s := botCheckSetup(t, botCheckCompose)
	s.Spec.BotCheck.Enabled = false
	out, err := Render(Input{Fleet: &fleet.Fleet{}, Stack: s, Template: tpl})
	must(t, err)
	if strings.Contains(string(out.Files[ComposeFile]), "anubis") || out.Files[AnubisPolicyFile] != nil {
		t.Errorf("off means off:\n%s", out.Files[ComposeFile])
	}
}

func TestBotCheckRefuses(t *testing.T) {
	for name, compose := range map[string]string{
		"a route to another port": "services:\n  web:\n    image: nginx:1.29\n    labels:\n      caddy: ${DOMAIN}\n      caddy.reverse_proxy: \"{{upstreams 80}}\"\n      caddy.handle_path: /admin/*\n      caddy.handle_path.reverse_proxy: \"{{upstreams 81}}\"\n",
		"no Caddy labels":         "services:\n  web:\n    image: nginx:1.29\n",
		"its own header_up":       "services:\n  web:\n    image: nginx:1.29\n    labels:\n      caddy: ${DOMAIN}\n      caddy.reverse_proxy: \"{{upstreams 80}}\"\n      caddy.reverse_proxy.header_up: X-Real-Ip 1.2.3.4\n",
		"a name taken":            strings.Replace(botCheckCompose, "networks:\n  ingress:", "  web-anubis: {image: nginx:1.29}\nnetworks:\n  ingress:", 1),
	} {
		tpl, s := botCheckSetup(t, compose)
		if _, err := Render(Input{Fleet: &fleet.Fleet{}, Stack: s, Template: tpl}); err == nil || !strings.Contains(err.Error(), "botCheck") {
			t.Errorf("%s: %v", name, err)
		}
	}
	tpl, s := botCheckSetup(t, botCheckCompose)
	s.Spec.Mode = "swarm"
	if _, err := Render(Input{Fleet: &fleet.Fleet{}, Stack: s, Template: tpl}); err == nil {
		t.Error("swarm isn't supported yet")
	}
	tpl, s = botCheckSetup(t, botCheckCompose)
	s.Spec.Access = &fleet.Access{Mode: "port", Port: 8060}
	if _, err := Render(Input{Fleet: &fleet.Fleet{}, Stack: s, Template: tpl, Host: &fleet.Host{}}); err == nil {
		t.Error("port mode has no Caddy to put Anubis behind")
	}
	tpl, s = botCheckSetup(t, botCheckCompose)
	tpl.Spec.Web = nil
	if _, err := Render(Input{Fleet: &fleet.Fleet{}, Stack: s, Template: tpl}); err == nil {
		t.Error("without spec.web there's no target")
	}
}

func TestBotCheckSequenceLabels(t *testing.T) {
	tpl, s := botCheckSetup(t, "services:\n  web:\n    image: nginx:1.29\n    labels: [\"caddy=${DOMAIN}\", \"caddy.reverse_proxy={{upstreams 80}}\", \"other=1\"]\n")
	out, err := Render(Input{Fleet: &fleet.Fleet{}, Stack: s, Template: tpl})
	must(t, err)
	c := string(out.Files[ComposeFile])
	if !strings.Contains(c, "{{upstreams 8923}}") || !strings.Contains(c, "other=1") || strings.Contains(c, "{{upstreams 80}}") {
		t.Errorf("compose:\n%s", c)
	}
}
