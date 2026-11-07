// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestParseTarget(t *testing.T) {
	for in, want := range map[string]Target{
		"ssh://me@nas.local:2222": {User: "me", Host: "nas.local", Port: 2222},
		"me@nas":                  {User: "me", Host: "nas"},
		"nas":                     {Host: "nas"},
		"ssh://10.0.0.5":          {Host: "10.0.0.5"},
	} {
		got, err := ParseTarget(in)
		if err != nil || got != want {
			t.Errorf("%s: %+v, %v", in, got, err)
		}
	}
	for _, bad := range []string{
		"-oProxyCommand=x", "ssh://-oProxyCommand=x", "ssh://me:secret@nas", "https://nas", "ssh://nas/path",
		"ssh://nas:99999", "ssh://-l@nas", "ssh://a;b", "", "ssh://$(x)",
	} {
		if got, err := ParseTarget(bad); err == nil {
			t.Errorf("%q must be refused, got %+v", bad, got)
		}
	}
}

func TestSSHArgs(t *testing.T) {
	args := SSH{}.Args(Target{User: "me", Host: "nas", Port: 2222})
	joined := strings.Join(args, " ")
	for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=yes", "-p 2222", "-l me"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if i := slices.Index(args, "--"); i < 0 || args[i+1] != "nas" {
		t.Errorf("the host must follow --: %v", args)
	}
}

func TestParseReport(t *testing.T) {
	r := parseReport([]byte("garbage line\nkernel=Linux\narch=x86_64\nos=Arch \x1b[31mLinux\nos_id=arch\nuser=me\nsudo=1\ndocker=1\ndocker_version=29.1\ncompose=2.40\nprojects=3\ncontainers=7\nswarm=manager\nswarm_cluster=abc\nunknown=x\nfleet=/srv/nepfleet\nnepfleet_api={\"name\":\"nas\"}\n"))
	if r.Kernel != "Linux" || !r.DockerRunning || r.Projects != 3 || r.Swarm != "manager" || !r.NepFleetRunning || !r.Sudo || r.Root {
		t.Errorf("%+v", r)
	}
	if strings.ContainsRune(r.OS, 0x1b) {
		t.Errorf("control characters must go: %q", r.OS)
	}
}

func TestNext(t *testing.T) {
	for _, c := range []struct {
		r    Report
		want string
	}{
		{Report{Kernel: "Darwin"}, "Linux hosts"},
		{Report{Kernel: "Linux"}, "No Docker"},
		{Report{Kernel: "Linux", Docker: true}, "can't reach"},
		{Report{Kernel: "Linux", Docker: true, DockerRunning: true}, "Compose"},
		{Report{Kernel: "Linux", Docker: true, DockerRunning: true, Compose: "2", Fleet: "/f"}, "connect"},
		{Report{Kernel: "Linux", Docker: true, DockerRunning: true, Compose: "2", Projects: 2}, "adopted"},
		{Report{Kernel: "Linux", Docker: true, DockerRunning: true, Compose: "2"}, "new fleet"},
	} {
		if got := c.r.Next(); !strings.Contains(got, c.want) {
			t.Errorf("%+v: %s", c.r, got)
		}
	}
}

// The script itself, run by a local shell in place of ssh.
func TestProbeScriptRuns(t *testing.T) {
	run := func(ctx context.Context, _ Target, script string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "sh", "-s")
		cmd.Stdin = strings.NewReader(script)
		return cmd.Output()
	}
	r, err := Probe(context.Background(), run, Target{Host: "here"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Kernel == "" || r.Arch == "" || r.User == "" {
		t.Errorf("%+v", r)
	}
}
