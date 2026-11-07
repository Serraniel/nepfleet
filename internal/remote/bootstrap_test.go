// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func texts(steps []Step) string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Text)
	}
	return strings.Join(out, "; ")
}

func TestBootstrapPlan(t *testing.T) {
	fresh := &Report{Kernel: "Linux", OSID: "debian", User: "me", Sudo: true}
	steps, err := BootstrapPlan(fresh, "nepfleet_ingress")
	if err != nil || len(steps) != 4 {
		t.Fatalf("%s: %v", texts(steps), err)
	}
	if !strings.Contains(steps[0].Script, "docker.io") || !strings.Contains(steps[2].Script, "usermod -aG docker me") {
		t.Errorf("%+v", steps)
	}
	if !strings.Contains(steps[2].Text, "effectively root") {
		t.Errorf("the docker group must be called what it is: %s", steps[2].Text)
	}

	// Ready: only the network, and no root needed for it.
	ready := &Report{Kernel: "Linux", OSID: "arch", User: "me", Docker: true, DockerRunning: true, Compose: "2"}
	steps, err = BootstrapPlan(ready, "nepfleet_ingress")
	if err != nil || len(steps) != 1 || steps[0].Root {
		t.Errorf("ready: %+v %v", steps, err)
	}

	// Compose only.
	steps, err = BootstrapPlan(&Report{Kernel: "Linux", OSID: "arch", User: "me", Sudo: true, Docker: true, DockerRunning: true}, "n")
	if err != nil || !strings.Contains(steps[0].Script, "docker-compose") || strings.Contains(steps[0].Script, "docker docker-compose") {
		t.Errorf("compose only: %+v %v", steps, err)
	}

	// Arch never does a partial upgrade.
	steps, _ = BootstrapPlan(&Report{Kernel: "Linux", OSID: "arch", User: "me", Sudo: true}, "n")
	if strings.Contains(steps[0].Script, "-Sy") {
		t.Errorf("partial upgrade: %s", steps[0].Script)
	}

	// As root: no group to join.
	steps, err = BootstrapPlan(&Report{Kernel: "Linux", OSID: "fedora", User: "root", Root: true}, "n")
	if err != nil || strings.Contains(texts(steps), "docker group") {
		t.Errorf("root: %s %v", texts(steps), err)
	}

	for name, r := range map[string]*Report{
		"not Linux":      {Kernel: "Darwin"},
		"unknown distro": {Kernel: "Linux", OSID: "plan9", User: "me", Sudo: true},
		"no way to root": {Kernel: "Linux", OSID: "debian", User: "me"},
		"odd user":       {Kernel: "Linux", OSID: "debian", User: "me; rm -rf /", Sudo: true},
	} {
		if _, err := BootstrapPlan(r, "n"); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if _, err := BootstrapPlan(ready, "x; reboot"); err == nil {
		t.Error("a network name must be checked before it reaches a shell")
	}
}

func TestStepScript(t *testing.T) {
	s := Step{Root: true, Script: "whoami\n"}
	if got := stepScript(s, false); !strings.HasPrefix(got, "sudo -n sh <<'") || !strings.Contains(got, "set -e\nwhoami\n") {
		t.Errorf("as a user: %q", got)
	}
	if got := stepScript(s, true); strings.Contains(got, "sudo") {
		t.Errorf("as root: %q", got)
	}
	if got := stepScript(Step{Script: "id\n"}, false); strings.Contains(got, "sudo") {
		t.Errorf("no root needed: %q", got)
	}
}

func TestRunBootstrapStops(t *testing.T) {
	var ran int
	run := func(_ context.Context, _ string) ([]byte, error) {
		ran++
		if ran == 2 {
			return nil, errors.New("boom")
		}
		return nil, nil
	}
	var b bytes.Buffer
	err := RunBootstrap(context.Background(), run, &Report{}, []Step{{Text: "one"}, {Text: "two"}, {Text: "three"}}, &b)
	if err == nil || ran != 2 || !strings.Contains(err.Error(), "step 2 (two)") {
		t.Errorf("%v, ran %d", err, ran)
	}
}
