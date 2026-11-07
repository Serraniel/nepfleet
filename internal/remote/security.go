// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"context"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
)

// Levels of a security finding.
const (
	LevelOK      = "ok"
	LevelWarn    = "warning"
	LevelBad     = "problem"
	LevelUnknown = "unknown" // this user can't tell
)

// Finding is one result of a host's security check.
type Finding struct {
	Check   string `json:"check"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// riskyPorts are services that shouldn't be open beyond the machine as a rule.
var riskyPorts = map[int]string{
	2375: "the Docker API without TLS: anyone reaching it is root", 2376: "the Docker API",
	3306: "MySQL or MariaDB", 5432: "PostgreSQL", 6379: "Redis", 27017: "MongoDB", 11211: "Memcached", 9200: "Elasticsearch",
}

// CheckSecurity looks at a host's security through a shell, over SSH or
// locally. It only reads: see securityScript.
func CheckSecurity(ctx context.Context, run func(context.Context, string) ([]byte, error)) ([]Finding, error) {
	out, err := run(ctx, securityScript)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(out), "done=1") {
		return nil, fmt.Errorf("no answer from the security check; does the machine have a POSIX shell?")
	}
	return parseSecurity(out), nil
}

// parseSecurity turns the script's key=value lines into findings. This text
// comes from another machine: only known keys count, and everything is cleaned.
func parseSecurity(out []byte) []Finding {
	vals := map[string][]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			vals[k] = append(vals[k], Clean(v))
		}
	}
	first := func(k string) string {
		if v := vals[k]; len(v) > 0 {
			return strings.ToLower(v[0])
		}
		return ""
	}
	var f []Finding
	add := func(check, level, msg string) { f = append(f, Finding{check, level, msg}) }

	// SSH. Without a value in a readable config, OpenSSH's defaults apply.
	switch {
	case first("ssh_off") == "1":
		add("ssh", LevelOK, "no SSH server runs here")
	case first("ssh_config") != "1":
		add("ssh-password", LevelUnknown, "the SSH server's config can't be read by this user")
		add("ssh-root", LevelUnknown, "the SSH server's config can't be read by this user")
	default:
		switch first("ssh_password") {
		case "no":
			add("ssh-password", LevelOK, "SSH refuses password logins")
		default:
			add("ssh-password", LevelBad, "SSH allows password logins: set PasswordAuthentication no, after checking your key works")
		}
		switch first("ssh_root") {
		case "no":
			add("ssh-root", LevelOK, "SSH refuses root logins")
		case "yes":
			add("ssh-root", LevelBad, "SSH allows root logins with a password: set PermitRootLogin no")
		default:
			add("ssh-root", LevelWarn, "SSH allows root logins with a key: PermitRootLogin no is stricter")
		}
	}

	// Firewall.
	active := vals["firewall"]
	switch {
	case len(active) == 0 && first("firewall_known") != "1":
		add("firewall", LevelUnknown, "can't tell whether a firewall runs")
	case len(active) == 0:
		add("firewall", LevelWarn, "no firewall service runs: everything listening is reachable, unless your network filters it")
	default:
		add("firewall", LevelOK, strings.Join(active, ", ")+" runs")
		if first("docker") == "1" {
			add("firewall-docker", LevelWarn, "Docker's published ports bypass "+strings.Join(active, ", ")+"'s usual rules: filter them in the DOCKER-USER chain")
		}
	}

	// What listens beyond the machine.
	var open []int
	seen := map[int]bool{}
	for _, addr := range vals["listen"] {
		host, portStr, err := net.SplitHostPort(strings.ReplaceAll(addr, "*", "0.0.0.0"))
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || seen[port] {
			continue
		}
		host, _, _ = strings.Cut(host, "%")
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			continue
		}
		seen[port] = true
		open = append(open, port)
	}
	slices.Sort(open)
	if first("listen_known") != "1" {
		add("ports", LevelUnknown, "can't list listening ports (ss is missing)")
	} else {
		var plain []string
		for _, p := range open {
			if what, risky := riskyPorts[p]; risky {
				add("ports", LevelBad, fmt.Sprintf("port %d (%s) listens beyond this machine", p, what))
				continue
			}
			plain = append(plain, strconv.Itoa(p))
		}
		if len(plain) > 0 {
			add("ports", LevelOK, "also listening beyond this machine, check you expect these: "+strings.Join(plain, ", "))
		}
	}

	// Updates.
	switch first("updates") {
	case "1":
		add("updates", LevelOK, "automatic security updates are on")
	case "manual":
		add("updates", LevelUnknown, "this distribution has no standard automatic updates to check; NepFleet's host updates are planned")
	default:
		add("updates", LevelWarn, "automatic security updates look off")
	}
	if first("reboot") == "1" {
		add("reboot", LevelWarn, "a reboot is pending, so updates may not be active yet")
	}

	// Docker.
	if first("docker") == "1" {
		if v := vals["privileged"]; len(v) > 0 {
			add("docker-privileged", LevelWarn, "privileged containers, each effectively root on this host: "+strings.Join(v, ", "))
		}
		if v := first("docker_group"); v != "" {
			add("docker-group", LevelWarn, "members of the docker group, each effectively root on this host: "+v)
		}
	}

	// Banning repeated failures.
	switch ban := vals["ban"]; {
	case len(ban) > 0:
		add("ban", LevelOK, strings.Join(ban, ", ")+" runs")
	default:
		add("ban", LevelWarn, "nothing bans repeated failed logins: consider the fail2ban template")
	}
	return f
}

// PrintFindings writes findings for a person, worst first.
func PrintFindings(w io.Writer, fs []Finding) {
	rank := map[string]int{LevelBad: 0, LevelWarn: 1, LevelUnknown: 2, LevelOK: 3}
	sorted := slices.Clone(fs)
	slices.SortStableFunc(sorted, func(a, b Finding) int { return rank[a.Level] - rank[b.Level] })
	mark := map[string]string{LevelBad: "✗", LevelWarn: "⚠", LevelUnknown: "?", LevelOK: "✓"}
	for _, x := range sorted {
		fmt.Fprintf(w, "  %s %s\n", mark[x.Level], x.Message)
	}
}

// securityScript only reads, and only asks what any user may ask: no sudo. It
// is POSIX sh, sent on stdin. A missing command leaves its value empty, which
// reads as "can't tell", never as "fine".
const securityScript = `
LC_ALL=C; export LC_ALL
kv() { printf '%s=%s\n' "$1" "$2"; }
has() { command -v "$1" >/dev/null 2>&1; }

# Only a stopped service counts as off: over SSH, it obviously runs.
if has systemctl; then
  a="$(systemctl is-active sshd 2>/dev/null)"; b="$(systemctl is-active ssh 2>/dev/null)"
  c="$(systemctl is-active ssh.socket 2>/dev/null)"; d="$(systemctl is-active sshd.socket 2>/dev/null)"
  if [ "$a" != active ] && [ "$b" != active ] && [ "$c" != active ] && [ "$d" != active ] && [ -z "$SSH_CONNECTION" ]; then
    kv ssh_off 1
  fi
fi

# First match wins in sshd_config; included files usually come first.
cfg=""
for f in /etc/ssh/sshd_config.d/*.conf /etc/ssh/sshd_config; do
  [ -r "$f" ] && cfg="$cfg $f"
done
if [ -n "$cfg" ]; then
  kv ssh_config 1
  kv ssh_password "$(grep -hiE '^[[:space:]]*PasswordAuthentication[[:space:]]' $cfg 2>/dev/null | head -n1 | awk '{print $2}')"
  kv ssh_root "$(grep -hiE '^[[:space:]]*PermitRootLogin[[:space:]]' $cfg 2>/dev/null | head -n1 | awk '{print $2}')"
fi

if has systemctl; then
  kv firewall_known 1
  for s in ufw firewalld nftables iptables; do
    [ "$(systemctl is-active "$s" 2>/dev/null)" = active ] && kv firewall "$s"
  done
  for s in fail2ban crowdsec; do
    [ "$(systemctl is-active "$s" 2>/dev/null)" = active ] && kv ban "$s"
  done
fi

if has ss; then
  kv listen_known 1
  ss -ltnH 2>/dev/null | awk '{print "listen=" $4}'
  ss -lunH 2>/dev/null | awk '{print "listen=" $4}'
fi

if has dpkg-query && dpkg-query -W -f='${Status}' unattended-upgrades 2>/dev/null | grep -q installed; then
  kv updates 1
elif has systemctl && [ "$(systemctl is-enabled dnf-automatic.timer 2>/dev/null)" = enabled ]; then
  kv updates 1
elif has pacman; then
  kv updates manual
fi
[ -e /var/run/reboot-required ] && kv reboot 1

if has docker && docker version >/dev/null 2>&1; then
  kv docker 1
  docker ps --format '{{.Names}}' 2>/dev/null | head -n 200 | while read -r c; do
    [ "$(docker inspect --format '{{.HostConfig.Privileged}}' "$c" 2>/dev/null)" = true ] && kv privileged "$c"
  done
  has getent && kv docker_group "$(getent group docker | cut -d: -f4)"
  for c in fail2ban crowdsec; do
    docker ps --format '{{.Image}}' 2>/dev/null | grep -q "$c" && kv ban "$c (container)"
  done
fi
kv done 1
`
