// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestPinData(t *testing.T) {
	compose := `services:
  db:
    image: postgres:18
    volumes: ["db:/var/lib/postgresql"]
  app:
    image: app:1
    volumes:
      - type: volume
        source: media
        target: /media
      - type: bind
        source: /etc/localtime
        target: /etc/localtime
  web:
    image: nginx:1.29
    volumes: ["./conf:/etc/nginx/conf.d:ro"]
  placed:
    image: x:1
    volumes: ["db:/x"]
    deploy:
      placement:
        constraints: ["node.hostname == nas"]
  managers:
    image: z:1
    volumes: ["db:/z"]
    deploy:
      placement:
        constraints: ["node.role == manager"]
  agent:
    image: y:1
    volumes: ["db:/y"]
    deploy: {mode: global}
  cache:
    image: redis:8
    volumes: ["cache:/data"]
volumes:
  db: {}
  media:
    driver_opts: {type: nfs, o: "addr=nas,rw", device: ":/media"}
  cache:
    name: app_cache
    external: true
`
	out, pinned, err := PinData([]byte(compose), "app")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pinned, ",") != "db,managers" {
		t.Fatalf("pinned %v", pinned)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	svc := doc["services"].(map[string]any)
	got := svc["db"].(map[string]any)["deploy"].(map[string]any)["placement"].(map[string]any)["constraints"]
	if c, _ := got.([]any); len(c) != 1 || c[0] != "node.labels.nepfleet.data.app==true" {
		t.Errorf("db constraints %v", got)
	}
	placed := svc["placed"].(map[string]any)["deploy"].(map[string]any)["placement"].(map[string]any)["constraints"].([]any)
	if len(placed) != 1 || placed[0] != "node.hostname == nas" {
		t.Errorf("a template's own placement changed: %v", placed)
	}
	managers := svc["managers"].(map[string]any)["deploy"].(map[string]any)["placement"].(map[string]any)["constraints"].([]any)
	if len(managers) != 2 || managers[0] != "node.role == manager" || managers[1] != "node.labels.nepfleet.data.app==true" {
		t.Errorf("a broad constraint: %v", managers)
	}
	for _, s := range []string{"app", "web", "cache"} {
		if _, has := svc[s].(map[string]any)["deploy"]; has {
			t.Errorf("%s got a deploy section", s)
		}
	}
	// Nothing to pin: the document comes back as it was.
	plain := "services:\n  web: {image: 'nginx:1.29'}\n"
	if out, pinned, err := PinData([]byte(plain), "app"); err != nil || pinned != nil || string(out) != plain {
		t.Errorf("plain: %q %v %v", out, pinned, err)
	}
}
