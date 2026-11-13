// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"testing"
	"time"

	"github.com/Serraniel/nepfleet"
)

// TestInstallForReal runs the whole plan on this machine, as it would over SSH,
// with the published image. Only on a CI runner: it writes /srv/nepfleet with
// sudo and starts a container.
func TestInstallForReal(t *testing.T) {
	if os.Getenv("CI") != "true" || os.Getenv("NEPFLEET_TEST_DOCKER") == "" {
		t.Skip("CI runners only: writes /srv/nepfleet")
	}
	if _, err := os.Stat(InstallDir); err == nil {
		t.Skip(InstallDir + " exists; not touching it")
	}
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	compose, err := PinCompose(nepfleet.Compose, "dev") // the latest release
	if err != nil {
		t.Fatal(err)
	}
	r := &Report{Kernel: "Linux", Arch: "x86_64", User: me.Username, Sudo: true, Docker: true, DockerRunning: true, Compose: "2"}
	steps, err := InstallPlan(r, "ci-host", compose, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	run := func(ctx context.Context, script string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "sh", "-s")
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("%s", out)
		}
		return out, err
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "compose", "-f", InstallDir+"/compose.yaml", "down", "-v").Run()
		_ = exec.Command("sudo", "-n", "rm", "-rf", InstallDir).Run()
	})
	var log strings.Builder
	if err := RunBootstrap(ctx, run, r, steps, &log, ""); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	res, err := http.Get("http://127.0.0.1:7480/healthz") //nolint:noctx // test
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("healthz: %d", res.StatusCode)
	}
	fleetYAML, err := os.ReadFile(InstallDir + "/fleet/fleet.yaml")
	if err != nil || !strings.Contains(string(fleetYAML), `age: ["age1`) {
		t.Errorf("the fleet doesn't encrypt to the new key:\n%s %v", fleetYAML, err)
	}
	if fi, err := os.Stat(InstallDir + "/age.key"); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("age.key: %v %v", fi, err)
	}
	// Running it again changes nothing and still succeeds.
	if err := RunBootstrap(ctx, run, r, steps, &log, ""); err != nil {
		t.Errorf("a second run: %v", err)
	}
}
