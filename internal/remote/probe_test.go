// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"os/exec"
	"runtime"
	"slices"
	"errors"
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
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell; on Windows the script runs on the remote only")
	}
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

func TestForwardArgs(t *testing.T) {
	args := SSH{}.ForwardArgs(Target{User: "me", Host: "nas", Port: 2222}, "/run/x.sock")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-N", "BatchMode=yes", "StrictHostKeyChecking=yes", "ExitOnForwardFailure=yes", "StreamLocalBindMask=0177", "-L /run/x.sock:127.0.0.1:7480"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if i := slices.Index(args, "--"); i != len(args)-2 || args[i+1] != "nas" {
		t.Errorf("the host must follow --, and nothing after it: %v", args)
	}
}

func TestMintedToken(t *testing.T) {
	tok, err := parseMinted([]byte("token 1a2b3c4d (control-x, apply) valid until 2026-11-08. It is shown only now:\nnft_abcdefghijklmnopqrstuvwxyz012345\n"))
	if err != nil || tok.ID != "1a2b3c4d" || tok.Secret != "nft_abcdefghijklmnopqrstuvwxyz012345" {
		t.Errorf("%+v %v", tok, err)
	}
	if _, err := parseMinted([]byte("sh: nepfleet: not found\n")); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("a failure must say why: %v", err)
	}
	if _, err := parseMinted([]byte("something odd\nnft_abcdefghijklmnopqrstuvwxyz012345\n")); err == nil || strings.Contains(err.Error(), "nft_") {
		t.Errorf("an error must not show a secret: %v", err)
	}
	s := SSH{}
	if _, err := s.MintToken(context.Background(), Target{Host: "x"}, "a; rm", "24h"); err == nil {
		t.Error("a label must be checked before it reaches a shell")
	}
	if err := s.RevokeToken(context.Background(), Target{Host: "x"}, "1; rm"); err == nil {
		t.Error("an ID must be checked before it reaches a shell")
	}
}

func TestSSHErrorNamesTheAgent(t *testing.T) {
	err := sshError(Target{Host: "vps"}, "user@vps: Permission denied (publickey).", errors.New("exit status 255"))
	if err == nil || !strings.Contains(err.Error(), "ssh-add") || !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("%v", err)
	}
}
