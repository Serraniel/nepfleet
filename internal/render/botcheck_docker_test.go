// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

// The rendered policy must load in the real Anubis, check a browser, and let an
// allowed path and a non-browser through.
func TestBotCheckRealAnubis(t *testing.T) {
	if os.Getenv("NEPFLEET_TEST_DOCKER") != "1" {
		t.Skip("set NEPFLEET_TEST_DOCKER=1")
	}
	dir := t.TempDir()
	policy := filepath.Join(dir, AnubisPolicyFile)
	must(t, os.WriteFile(policy, anubisPolicy(&fleet.BotCheck{Allow: []string{"^/api/"}, Difficulty: 4}), 0o644)) //nolint:gosec // read by the container's user
	name := "nepfleet-test-anubis-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	docker := func(args ...string) (string, error) {
		out, err := exec.Command("docker", args...).CombinedOutput() //nolint:gosec,noctx // test
		return string(out), err
	}
	_, _ = docker("rm", "-f", name+"-web", name+"-anubis")
	_, _ = docker("network", "rm", name)
	if out, err := docker("network", "create", name); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	t.Cleanup(func() {
		_, _ = docker("rm", "-f", name+"-web", name+"-anubis")
		_, _ = docker("network", "rm", name)
	})
	if out, err := docker("run", "-d", "--name", name+"-web", "--network", name, "--network-alias", "web",
		"nginx:alpine", "sh", "-c", "d=/usr/share/nginx/html; mkdir -p $d/api && echo app > $d/index.html && echo api > $d/api/x && exec nginx -g 'daemon off;'"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if out, err := docker("run", "-d", "--name", name+"-anubis", "--network", name, "--read-only", "--tmpfs", "/tmp",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"-e", "BIND=:8923", "-e", "TARGET=http://web:80", "-e", "POLICY_FNAME=/data/cfg/"+AnubisPolicyFile,
		"-v", policy+":/data/cfg/"+AnubisPolicyFile+":ro", anubisImage); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	get := func(ua, path string) string {
		out, _ := docker("run", "--rm", "--network", name, "alpine:3.24", "wget", "-q", "-O", "-",
			"--header", "X-Real-Ip: 203.0.113.7", "-U", ua, "http://"+name+"-anubis:8923"+path)
		return out
	}
	var page string
	for range 30 {
		if page = get("Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0", "/"); page != "" {
			break
		}
		time.Sleep(time.Second)
	}
	if strings.TrimSpace(page) == "app" || !strings.Contains(strings.ToLower(page), "anubis") {
		logs, _ := docker("logs", name+"-anubis")
		t.Fatalf("a browser must get the challenge, got %q\n%s", page, logs)
	}
	if got := get("Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0", "/api/x"); strings.TrimSpace(got) != "api" {
		t.Errorf("an allowed path must go straight through: %q", got)
	}
	if got := get("some-app/1.0", "/"); strings.TrimSpace(got) != "app" {
		t.Errorf("a non-browser client isn't checked: %q", got)
	}
}
