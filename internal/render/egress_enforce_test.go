// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/egress"
	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

type egressDoc struct {
	Services map[string]struct {
		NetworkMode string         `yaml:"network_mode"`
		Ports       []string       `yaml:"ports"`
		Networks    map[string]any `yaml:"networks"`
		Labels      map[string]string
		CapDrop     []string       `yaml:"cap_drop"`
		DependsOn   map[string]any `yaml:"depends_on"`
		Environment map[string]string
	} `yaml:"services"`
}

func egressRender(t *testing.T, compose string, allow ...string) egressDoc {
	t.Helper()
	out, err := WithEgress([]byte(compose), allow)
	if err != nil {
		t.Fatal(err)
	}
	var d egressDoc
	if err := yaml.Unmarshal(out, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestWithEgressMovesTheNetwork(t *testing.T) {
	d := egressRender(t, `services:
  web:
    image: nginx
    ports: ["8080:80"]
    networks: [nepfleet_ingress]
    depends_on: [db]
    labels:
      caddy: example.test
      caddy.reverse_proxy: "{{upstreams 80}}"
      other: stays
  db:
    image: postgres
    cap_drop: [ALL]
  helper:
    image: x
    network_mode: service:web
`, "smtp.example.test", "203.0.113.5", "2001:db8::/32")
	web, side := d.Services["web"], d.Services["web-egress"]
	if web.NetworkMode != "service:web-egress" || len(web.Ports) != 0 || web.Networks != nil {
		t.Errorf("web keeps its own network: %+v", web)
	}
	if len(side.Ports) != 1 || side.Networks["nepfleet_ingress"] == nil {
		t.Errorf("the sidecar must own ports and networks: %+v", side)
	}
	if side.Labels["caddy"] == "" || web.Labels["caddy"] != "" || web.Labels["other"] != "stays" {
		t.Errorf("caddy labels: web %v, sidecar %v", web.Labels, side.Labels)
	}
	if web.DependsOn["db"] == nil || web.DependsOn["web-egress"] == nil {
		t.Errorf("depends_on: %v", web.DependsOn)
	}
	if h := d.Services["helper"].NetworkMode; h != "service:web-egress" {
		t.Errorf("namespaces don't chain: %s", h)
	}
	if e := side.Environment; e["NEPFLEET_EGRESS_HOSTS"] != "smtp.example.test" || e["NEPFLEET_EGRESS_V4"] != "203.0.113.5" || e["NEPFLEET_EGRESS_V6"] != "2001:db8::/32" {
		t.Errorf("allow: %v", e)
	}
	if !strings.Contains(strings.Join(web.CapDrop, ","), "NET_RAW") {
		t.Errorf("packet sockets pass by iptables: %v", web.CapDrop)
	}
	if got := d.Services["db"].CapDrop; len(got) != 1 {
		t.Errorf("cap_drop ALL already covers it: %v", got)
	}
}

func TestWithEgressRefuses(t *testing.T) {
	for name, compose := range map[string]string{
		"host network": "services:\n  a:\n    image: x\n    network_mode: host\n",
		"other mode":   "services:\n  a:\n    image: x\n    network_mode: bridge\n",
		"container":    "services:\n  a:\n    image: x\n    network_mode: container:other\n",
		"privileged":   "services:\n  a:\n    image: x\n    privileged: true\n",
		"net admin":    "services:\n  a:\n    image: x\n    cap_add: [cap_net_admin]\n",
		"all caps":     "services:\n  a:\n    image: x\n    cap_add: [ALL]\n",
		"name taken":   "services:\n  a:\n    image: x\n  a-egress:\n    image: x\n",
	} {
		if _, err := WithEgress([]byte(compose), nil); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	for _, bad := range []string{"a b", "$(reboot)", "*.example.test", "a;b"} {
		if _, err := WithEgress([]byte("services:\n  a:\n    image: x\n"), []string{bad}); err == nil {
			t.Errorf("%q must not reach a shell", bad)
		}
	}
}

func TestEgressAllow(t *testing.T) {
	tpl := &template.Template{}
	tpl.Spec.Egress = []template.EgressHost{{Host: "huggingface.co", Why: "models"}}
	tpl.Spec.Inputs = []template.Input{{Name: "ISSUER", Type: "url"}, {Name: "NAME", Type: "string"}, {Name: "EMPTY", Type: "url"}}
	got := EgressAllow([]string{"own.example.test"}, tpl, map[string]string{"ISSUER": "https://id.example.test:8443/realm", "NAME": "https://no.example.test"})
	if strings.Join(got, " ") != "huggingface.co id.example.test own.example.test" {
		t.Errorf("%v", got)
	}
}

func TestEnforceEgressModes(t *testing.T) {
	compose := []byte("services:\n  a:\n    image: x\n")
	stack := &fleet.Stack{}
	if out, err := enforceEgress(compose, Input{Stack: stack}, nil); err != nil || string(out) != string(compose) {
		t.Errorf("off is the default: %s %v", out, err)
	}
	stack.Spec.Egress = &fleet.Egress{Mode: fleet.EgressEnforce}
	if out, err := enforceEgress(compose, Input{Stack: stack}, nil); err != nil || !strings.Contains(string(out), "a-egress") {
		t.Errorf("enforce: %s %v", out, err)
	}
	stack.Spec.Mode = "swarm"
	if _, err := enforceEgress(compose, Input{Stack: stack}, nil); err == nil {
		t.Error("swarm must be refused, not left unfiltered")
	}
}

func TestEgressLogBlocksNothing(t *testing.T) {
	// Modes enforce refuses go through, as nothing here can be undone by them.
	out, err := WithEgressLog([]byte("services:\n  a:\n    image: x\n    privileged: true\n    cap_add: [NET_ADMIN]\n"))
	if err != nil {
		t.Fatal(err)
	}
	all := string(out)
	if !strings.Contains(all, "tcpdump") || strings.Contains(all, "iptables") || strings.Contains(all, "NEPFLEET_EGRESS_HOSTS") {
		t.Errorf("%s", all)
	}
	if strings.Contains(all, "cap_drop:\n      - \"NET_RAW\"") {
		t.Errorf("the service keeps what it had: %s", all)
	}
	stack := &fleet.Stack{}
	stack.Spec.Egress = &fleet.Egress{Mode: fleet.EgressLog}
	compose := []byte("services:\n  a:\n    image: x\n")
	if got, err := enforceEgress(compose, Input{Stack: stack}, nil); err != nil || !strings.Contains(string(got), "a-egress") {
		t.Errorf("log: %s %v", got, err)
	}
	stack.Spec.Mode = "swarm"
	if _, err := enforceEgress(compose, Input{Stack: stack}, nil); err == nil {
		t.Error("swarm must be refused")
	}
}

// With real containers: what the sidecar writes is what Parse reads.
func TestEgressLogInDocker(t *testing.T) {
	if os.Getenv("NEPFLEET_TEST_DOCKER") == "" {
		t.Skip("set NEPFLEET_TEST_DOCKER to run with Docker and internet access")
	}
	out, err := WithEgressLog([]byte(`services:
  app:
    image: alpine:3.22
    command: ["sh", "-c", "sleep 2; wget -q -T 5 -O /dev/null http://one.one.one.one/; nc -z -w 3 9.9.9.9 443; sleep 600"]
`))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(file, out, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	dc := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", append([]string{"compose", "-p", "nepfleet-egress-log-test", "-f", file}, args...)...).CombinedOutput()
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-p", "nepfleet-egress-log-test", "-f", file, "down", "-v", "-t", "1").Run()
	})
	if o, err := dc("up", "-d", "--wait"); err != nil {
		t.Fatalf("%s: %v", o, err)
	}
	var report egress.Report
	var logs []byte
	for range 20 {
		if logs, err = dc("logs", "--no-log-prefix", "app-egress"); err != nil {
			t.Fatal(err)
		}
		report = egress.Parse(string(logs))
		if slices.Contains(report.Hosts, "one.one.one.one") && slices.Contains(report.Addresses, "9.9.9.9") {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Errorf("%+v from:\n%s", report, logs)
}

// With real containers and the internet: what is denied, what still works, and
// that the rules outlive a restart of the service.
func TestEgressInDocker(t *testing.T) {
	if os.Getenv("NEPFLEET_TEST_DOCKER") == "" {
		t.Skip("set NEPFLEET_TEST_DOCKER to run with Docker and internet access")
	}
	out, err := WithEgress([]byte(`services:
  app:
    image: alpine:3.22
    command: ["sleep", "600"]
  peer:
    image: alpine:3.22
    command: ["sh", "-c", "while :; do echo ok | nc -l -p 8080; done"]
`), []string{"1.1.1.1", "one.one.one.one"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(file, out, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dc := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "docker", append([]string{"compose", "-p", "nepfleet-egress-test", "-f", file}, args...)...).CombinedOutput()
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-p", "nepfleet-egress-test", "-f", file, "down", "-v", "-t", "1").Run()
	})
	if o, err := dc("up", "-d", "--wait"); err != nil {
		t.Fatalf("%s: %v", o, err)
	}
	check := func(what string, want bool, host, port string) {
		t.Helper()
		_, err := dc("exec", "-T", "app", "nc", "-z", "-w", "5", host, port)
		if (err == nil) != want {
			t.Errorf("%s: reachable %v, want %v", what, err == nil, want)
		}
	}
	verify := func() {
		t.Helper()
		check("an allowed address", true, "1.1.1.1", "443")
		check("an allowed name", true, "1.0.0.1", "443")
		check("anything else", false, "9.9.9.9", "443")
		check("a peer by its service name", true, "peer", "8080")
	}
	verify()
	// The refusal is in the sidecar's log, for nepfleet egress suggest.
	var logs []byte
	for range 10 {
		logs, _ = dc("logs", "--no-log-prefix", "app-egress")
		if bytes.Contains(logs, []byte("> 9.9.9.9.443")) {
			break
		}
		time.Sleep(time.Second)
	}
	if !bytes.Contains(logs, []byte("> 9.9.9.9.443")) {
		t.Errorf("the denial isn't logged:\n%s", logs)
	}
	if o, err := dc("restart", "app"); err != nil {
		t.Fatalf("%s: %v", o, err)
	}
	verify()
}
