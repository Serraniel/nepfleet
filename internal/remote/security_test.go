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
