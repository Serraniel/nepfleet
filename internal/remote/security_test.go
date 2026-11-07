// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func level(fs []Finding, check string) string {
	for _, f := range fs {
		if f.Check == check {
			return f.Level
		}
	}
	return ""
}

func TestSecurityFindings(t *testing.T) {
	// A hardened machine.
	good := parseSecurity([]byte("ssh_config=1\nssh_password=no\nssh_root=no\nfirewall_known=1\nfirewall=nftables\nban=fail2ban\nlisten_known=1\nlisten=127.0.0.1:5432\nlisten=[::1]:6379\nlisten=0.0.0.0:443\nupdates=1\ndone=1\n"))
	for check, want := range map[string]string{"ssh-password": LevelOK, "ssh-root": LevelOK, "firewall": LevelOK, "updates": LevelOK, "ban": LevelOK} {
		if got := level(good, check); got != want {
			t.Errorf("good %s: %s, want %s", check, got, want)
		}
	}
	for _, f := range good {
		if f.Level == LevelBad {
			t.Errorf("a database on loopback is fine: %+v", f)
		}
	}

	// A careless one. No line in a readable config means OpenSSH's defaults.
	bad := parseSecurity([]byte("ssh_config=1\nssh_password=\nssh_root=yes\nfirewall_known=1\nlisten_known=1\nlisten=0.0.0.0:5432\nlisten=[::]:2375\nlisten=*:6379\nlisten=0.0.0.0:5432\nreboot=1\ndocker=1\nprivileged=watchtower\ndocker_group=alice,bob\ndone=1\n"))
	for check, want := range map[string]string{"ssh-password": LevelBad, "ssh-root": LevelBad, "firewall": LevelWarn, "ports": LevelBad, "reboot": LevelWarn, "docker-privileged": LevelWarn, "docker-group": LevelWarn, "ban": LevelWarn, "updates": LevelWarn} {
		if got := level(bad, check); got != want {
			t.Errorf("bad %s: %s, want %s", check, got, want)
		}
	}
	var b bytes.Buffer
	PrintFindings(&b, bad)
	out := b.String()
	if strings.Count(out, "PostgreSQL") != 1 {
		t.Errorf("a port listed twice must count once:\n%s", out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "✗") {
		t.Errorf("worst first:\n%s", out)
	}

	// Without rights, it says so instead of calling it fine.
	blind := parseSecurity([]byte("done=1\n"))
	for _, check := range []string{"ssh-password", "ssh-root", "firewall", "ports"} {
		if got := level(blind, check); got != LevelUnknown {
			t.Errorf("blind %s: %s", check, got)
		}
	}

	// No SSH server: its config doesn't matter.
	off := parseSecurity([]byte("ssh_off=1\nssh_config=1\nssh_password=yes\ndone=1\n"))
	if level(off, "ssh") != LevelOK || level(off, "ssh-password") != "" {
		t.Errorf("no server: %+v", off)
	}

	// Firewall plus Docker: the surprise worth telling.
	if got := level(parseSecurity([]byte("firewall_known=1\nfirewall=ufw\ndocker=1\ndone=1\n")), "firewall-docker"); got != LevelWarn {
		t.Errorf("firewall-docker: %s", got)
	}

	// Remote text can't drive the terminal.
	for _, f := range parseSecurity([]byte("docker=1\nprivileged=\x1b[2Jevil\ndone=1\n")) {
		if strings.ContainsRune(f.Message, 0x1b) {
			t.Errorf("control characters: %q", f.Message)
		}
	}
}

func TestPublishedPorts(t *testing.T) {
	out := []byte("listen_known=1\nlisten=0.0.0.0:5432\nlisten=0.0.0.0:443\nlisten=0.0.0.0:8080\nlisten=0.0.0.0:9000\nlisten=0.0.0.0:22\n" +
		"published=shop|0.0.0.0:5432->5432/tcp, [::]:5432->5432/tcp\n" +
		"published=web|0.0.0.0:443->443/tcp\n" +
		"published=other|0.0.0.0:8080-8081->8080-8081/tcp, 127.0.0.1:7000->7000/tcp\n" +
		"published=|0.0.0.0:9000->9000/tcp\n" +
		"published=quiet|\ndone=1\n")
	text := func(fs []Finding) string {
		var b bytes.Buffer
		PrintFindings(&b, fs)
		return b.String()
	}
	got := text(parseSecurityFor(out, []string{"web"}))
	for _, want := range []string{
		"port 5432 (PostgreSQL) listens beyond this machine, published by Compose project shop: publish it as 127.0.0.1:5432",
		"port 8080 is published by Compose project other, which this fleet doesn't manage",
		"port 9000 is published by a container outside any Compose project",
		"check you expect these: 22, 443",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// With no fleet to compare with, nothing is called unmanaged.
	if got := text(parseSecurity(out)); strings.Contains(got, "doesn't manage") {
		t.Errorf("no fleet:\n%s", got)
	}
	// Odd text from another machine.
	for _, line := range []string{"x|0.0.0.0:99999999999999999999->1/tcp", "x|0.0.0.0:9-1->1/tcp", "x|0.0.0.0:1-999999->1/tcp", "nonsense"} {
		if owners := publishedPorts([]string{line}); len(owners) > 1 {
			t.Errorf("%q: %d ports", line, len(owners))
		}
	}
}

func TestNewProblems(t *testing.T) {
	fs := []Finding{{Level: LevelBad, Message: "a"}, {Level: LevelBad, Message: "b"}, {Level: LevelWarn, Message: "c"}}
	fresh, now := NewProblems(fs, []string{"a", "gone"})
	if len(fresh) != 1 || fresh[0].Message != "b" || len(now) != 2 {
		t.Errorf("%+v %v", fresh, now)
	}
	// Fixed, then back: reported again.
	_, now = NewProblems(nil, now)
	if fresh, _ := NewProblems(fs, now); len(fresh) != 2 {
		t.Errorf("%+v", fresh)
	}
}

func TestSecurityScriptRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	got, err := CheckSecurity(context.Background(), func(ctx context.Context, script string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "sh", "-s")
		cmd.Stdin = strings.NewReader(script)
		return cmd.Output()
	})
	if err != nil || len(got) == 0 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := CheckSecurity(context.Background(), func(context.Context, string) ([]byte, error) { return []byte("garbage"), nil }); err == nil {
		t.Error("an incomplete answer must not read as a clean bill of health")
	}
}
