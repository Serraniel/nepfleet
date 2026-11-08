// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const testCompose = "services:\n  nepfleet:\n    image: ghcr.io/serraniel/nepfleet:latest   # or :edge\n    user: \"${NEPFLEET_UID}\"\n"

func TestPinCompose(t *testing.T) {
	for version, want := range map[string]string{
		"v0.1.0":           "ghcr.io/serraniel/nepfleet:0.1.0   # or :edge",
		"0.2.3":            "ghcr.io/serraniel/nepfleet:0.2.3   # or :edge",
		"dev":              "ghcr.io/serraniel/nepfleet:latest   # or :edge",
		"0.2.0-rc.1-dirty": "ghcr.io/serraniel/nepfleet:latest   # or :edge",
	} {
		got, err := PinCompose([]byte(testCompose), version)
		if err != nil || !strings.Contains(string(got), want) || !strings.Contains(string(got), "${NEPFLEET_UID}") {
			t.Errorf("%s: %v\n%s", version, err, got)
		}
	}
	if _, err := PinCompose([]byte("services: {}\n"), "0.1.0"); err == nil {
		t.Error("no image line")
	}
}

func readyReport() *Report {
	return &Report{Kernel: "Linux", Arch: "x86_64", User: "me", Sudo: true, Docker: true, DockerRunning: true, Compose: "2.40.0"}
}

func TestInstallPlan(t *testing.T) {
	steps, err := InstallPlan(readyReport(), "vps", []byte(testCompose), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 6 || !steps[0].Root || steps[1].Root {
		t.Fatalf("%+v", steps)
	}
	all := ""
	for _, s := range steps {
		all += s.Script
		// Every script is valid shell, as the remote runs it.
		if path, err := exec.LookPath("sh"); err == nil {
			f := t.TempDir() + "/step.sh"
			if err := os.WriteFile(f, []byte(stepScript(s, false)), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command(path, "-n", f).CombinedOutput(); err != nil { //nolint:gosec,noctx // test
				t.Errorf("%s: %v %s", s.Text, err, out)
			}
		}
	}
	for _, want := range []string{"chown me: /srv/nepfleet", "NEPFLEET_HOST=%s\\n", " vps > .env", "${NEPFLEET_UID}", "age-keygen", "init .", "up -d --wait"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q", want)
		}
	}
	if skipped, err := InstallPlan(readyReport(), "vps", []byte(testCompose), true); err != nil || len(skipped) != 5 || skipped[0].Root {
		t.Errorf("skip-dir: %+v %v", skipped, err)
	}
}

func TestInstallPlanRefuses(t *testing.T) {
	for name, change := range map[string]func(*Report){
		"not Linux":        func(r *Report) { r.Kernel = "Darwin" },
		"armv7":            func(r *Report) { r.Arch = "armv7l" },
		"already running":  func(r *Report) { r.NepFleetRunning = true },
		"a fleet is there": func(r *Report) { r.Fleet = "/srv/nepfleet/fleet" },
		"no Docker":        func(r *Report) { r.Docker = false },
		"no Compose":       func(r *Report) { r.Compose = "" },
		"no Docker access": func(r *Report) { r.DockerRunning = false },
		"no root":          func(r *Report) { r.Sudo = false },
	} {
		r := readyReport()
		change(r)
		if _, err := InstallPlan(r, "vps", []byte(testCompose), false); err == nil {
			t.Errorf("%s: no refusal", name)
		}
	}
	if _, err := InstallPlan(readyReport(), "Bad Name", []byte(testCompose), false); err == nil {
		t.Error("a bad host name")
	}
	if _, err := InstallPlan(readyReport(), "vps", []byte(testCompose+composeEOF+"\n"), false); err == nil {
		t.Error("the heredoc marker in compose.yaml")
	}
	r := readyReport()
	r.Sudo = false
	if _, err := InstallPlan(r, "vps", []byte(testCompose), true); err != nil {
		t.Errorf("without root, --skip-dir goes ahead: %v", err)
	}
}
