// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// docker compose config names the project at the top; docker stack deploy
// refuses it.
func TestForSwarmDropsProjectName(t *testing.T) {
	out, err := ForSwarm([]byte("name: hb\nservices:\n  web:\n    image: nginx:1.29\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "name: hb") || !strings.Contains(string(out), "services:") {
		t.Errorf("%s", out)
	}
}

func TestForSwarmTmpfsAndPorts(t *testing.T) {
	in := "services:\n  proxy:\n    image: p:1\n    read_only: true\n    tmpfs: [/run, \"/tmp:size=1m\"]\n    volumes: [\"/var/run/docker.sock:/var/run/docker.sock:ro\"]\n" +
		"  caddy:\n    image: c:1\n    ports:\n      - mode: ingress\n        target: 80\n        published: \"80\"\n        protocol: tcp\n      - \"443:443\"\n"
	out, err := ForSwarm([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services map[string]struct {
			Tmpfs   any   `yaml:"tmpfs"`
			Volumes []any `yaml:"volumes"`
			Ports   []any `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	proxy := doc.Services["proxy"]
	if proxy.Tmpfs != nil || len(proxy.Volumes) != 3 {
		t.Fatalf("proxy: %+v", proxy)
	}
	if proxy.Volumes[0] != "/var/run/docker.sock:/var/run/docker.sock:ro" {
		t.Errorf("bind kept: %v", proxy.Volumes[0])
	}
	for i, want := range []string{"/run", "/tmp"} {
		m, _ := proxy.Volumes[i+1].(map[string]any)
		if m["type"] != "tmpfs" || m["target"] != want {
			t.Errorf("mount %d: %v", i, proxy.Volumes[i+1])
		}
	}
	port, _ := doc.Services["caddy"].Ports[0].(map[string]any)
	if p, ok := port["published"].(int); !ok || p != 80 {
		t.Errorf("published: %#v", port["published"])
	}
	if doc.Services["caddy"].Ports[1] != "443:443" {
		t.Errorf("short syntax changed: %v", doc.Services["caddy"].Ports[1])
	}
}

func TestForSwarmDropsProfiles(t *testing.T) {
	out, err := ForSwarm([]byte("services:\n  gotenberg:\n    image: g:1\n    profiles: [office]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "profiles") || !strings.Contains(string(out), "gotenberg") {
		t.Errorf("%s", out)
	}
}
