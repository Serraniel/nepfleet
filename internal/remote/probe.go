// SPDX-License-Identifier: AGPL-3.0-or-later

// Package remote looks at and connects to machines over SSH.
package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Target is a machine to reach over SSH. Host may be an alias from the
// user's ~/.ssh/config.
type Target struct {
	User string `json:"user,omitempty"`
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)
var userRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// ParseTarget reads ssh://[user@]host[:port] or [user@]host. Names are
// checked strictly: they end up on ssh's command line, where a leading "-"
// would be an option.
func ParseTarget(s string) (Target, error) {
	if !strings.Contains(s, "://") {
		s = "ssh://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "ssh" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return Target{}, fmt.Errorf("%q is not ssh://[user@]host[:port]", s)
	}
	if _, hasPass := u.User.Password(); hasPass {
		return Target{}, errors.New("do not put a password in the address: NepFleet uses your SSH keys or agent")
	}
	t := Target{Host: u.Hostname(), User: u.User.Username()}
	if !hostRe.MatchString(t.Host) {
		return Target{}, fmt.Errorf("%q is not a host name", t.Host)
	}
	if t.User != "" && !userRe.MatchString(t.User) {
		return Target{}, fmt.Errorf("%q is not a user name", t.User)
	}
	if p := u.Port(); p != "" {
		t.Port, err = strconv.Atoi(p)
		if err != nil || t.Port < 1 || t.Port > 65535 {
			return Target{}, fmt.Errorf("%q is not a port", p)
		}
	}
	return t, nil
}

func (t Target) String() string {
	s := "ssh://"
	if t.User != "" {
		s += t.User + "@"
	}
	s += t.Host
	if t.Port != 0 {
		s += ":" + strconv.Itoa(t.Port)
	}
	return s
}

// SSH runs a script on a target with the system's ssh client, so the user's
// keys, agent and config apply.
type SSH struct {
	// ConfigFile, if set, replaces ~/.ssh/config.
	ConfigFile string
}

// Args is ssh's command line. BatchMode: never prompt, as a daemon has nobody
// to ask. An unknown host key is refused, never accepted: the first connection
// is what would let someone in the middle in.
func (s SSH) Args(t Target) []string {
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10"}
	if s.ConfigFile != "" {
		args = append(args, "-F", s.ConfigFile)
	}
	if t.Port != 0 {
		args = append(args, "-p", strconv.Itoa(t.Port))
	}
	if t.User != "" {
		args = append(args, "-l", t.User)
	}
	return append(args, "--", t.Host, "sh", "-s")
}

// maxOutput bounds what is read from a remote machine.
const maxOutput = 64 << 10

// Run sends the script on stdin and returns its output.
func (s SSH) Run(ctx context.Context, t Target, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ssh", s.Args(t)...) //nolint:gosec // names checked in ParseTarget, after "--"
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = &limitWriter{w: &stderr, n: 4 << 10}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ssh: %w", err)
	}
	out, rerr := io.ReadAll(io.LimitReader(stdout, maxOutput))
	if rerr == nil {
		_, _ = io.Copy(io.Discard, stdout) // let ssh finish
	}
	if err := cmd.Wait(); err != nil {
		return nil, sshError(t, Clean(stderr.String()), err)
	}
	return out, rerr
}

func sshError(t Target, stderr string, err error) error {
	switch {
	case strings.Contains(stderr, "Host key verification failed"), strings.Contains(stderr, "No ED25519 host key"), strings.Contains(stderr, "host key is not known"):
		return fmt.Errorf("%s: its host key is not known or has changed. Connect once with ssh and check the fingerprint: NepFleet never accepts a key on its own", t)
	case strings.Contains(stderr, "Permission denied"):
		return fmt.Errorf("%s: SSH refused the login; NepFleet uses your SSH keys or agent and never asks for a password", t)
	case stderr != "":
		return fmt.Errorf("%s: %s", t, stderr)
	}
	return fmt.Errorf("%s: %w", t, err)
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

// Clean makes text from a remote machine safe to show: no control characters
// (a remote could otherwise drive the terminal), bounded length.
func Clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	const maxLen = 200
	if r := []rune(s); len(r) > maxLen {
		s = string(r[:maxLen]) + "…"
	}
	return s
}

// Report is what a probe found.
type Report struct {
	Target string `json:"target"`
	OS     string `json:"os"`     // PRETTY_NAME of os-release, or uname
	OSID   string `json:"osId"`   // ID of os-release: arch, debian, …
	Kernel string `json:"kernel"` // Linux, Darwin, …
	Arch   string `json:"arch"`   // x86_64, aarch64, …
	User   string `json:"user"`
	Root   bool   `json:"root"`
	Sudo   bool   `json:"sudo"` // sudo without a password

	Docker        bool   `json:"docker"`        // the docker command exists
	DockerRunning bool   `json:"dockerRunning"` // and this user reaches the daemon
	DockerVersion string `json:"dockerVersion,omitempty"`
	Compose       string `json:"compose,omitempty"` // version of the compose plugin
	Projects      int    `json:"projects"`          // running compose projects
	Containers    int    `json:"containers"`        // running containers

	Swarm        string `json:"swarm,omitempty"` // manager, worker
	SwarmCluster string `json:"swarmCluster,omitempty"`

	NepFleet        string `json:"nepfleet,omitempty"`      // version of the installed CLI
	NepFleetRunning bool   `json:"nepfleetRunning"`         // a server answers on its default port
	NepFleetAPI     string `json:"nepfleetApi,omitempty"`   // what it says about itself
	NepFleetImage   string `json:"nepfleetImage,omitempty"` // a running NepFleet container
	Fleet           string `json:"fleet,omitempty"`         // a fleet repository found here
}

// Probe looks at a target. It only reads: see probeScript.
func Probe(ctx context.Context, run func(context.Context, Target, string) ([]byte, error), t Target) (*Report, error) {
	out, err := run(ctx, t, probeScript)
	if err != nil {
		return nil, err
	}
	r := parseReport(out)
	if r.Kernel == "" {
		return nil, fmt.Errorf("%s: no answer from the probe; does it have a POSIX shell?", t)
	}
	r.Target = t.String()
	return r, nil
}

// parseReport reads the script's key=value lines. Anything else is ignored:
// this text comes from another machine.
func parseReport(out []byte) *Report {
	r := &Report{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = Clean(v)
		yes := v == "1"
		n, _ := strconv.Atoi(v)
		switch k {
		case "kernel":
			r.Kernel = v
		case "arch":
			r.Arch = v
		case "os":
			r.OS = v
		case "os_id":
			r.OSID = v
		case "user":
			r.User = v
		case "root":
			r.Root = yes
		case "sudo":
			r.Sudo = yes
		case "docker":
			r.Docker = yes
		case "docker_version":
			r.DockerVersion = v
			r.DockerRunning = v != ""
		case "compose":
			r.Compose = v
		case "projects":
			r.Projects = n
		case "containers":
			r.Containers = n
		case "swarm":
			r.Swarm = v
		case "swarm_cluster":
			r.SwarmCluster = v
		case "nepfleet":
			r.NepFleet = v
		case "nepfleet_api":
			r.NepFleetAPI = v
			r.NepFleetRunning = v != ""
		case "nepfleet_image":
			r.NepFleetImage = v
		case "fleet":
			r.Fleet = v
		}
	}
	return r
}

// Next says what fits this machine, for the wizard and the CLI.
func (r *Report) Next() string {
	switch {
	case r.Kernel != "Linux":
		return "NepFleet manages Linux hosts; this is " + r.Kernel + "."
	case !r.Docker:
		return "No Docker yet: it needs installing first, then a new fleet can be set up here."
	case !r.DockerRunning:
		return "Docker is installed, but this user can't reach it: start it, or add the user to the docker group."
	case r.Compose == "":
		return "Docker runs, but the Compose plugin is missing."
	case r.NepFleetRunning || r.Fleet != "":
		return "A NepFleet is here: connect to it."
	case r.Projects > 0:
		return "Docker runs projects without NepFleet: they can be adopted with nepfleet import."
	}
	return "Docker is ready: a new fleet can be set up here."
}

// probeScript only reads. It is POSIX sh, sent on stdin, so nothing is left on
// the remote. Every command may be missing: errors are dropped, and an unknown
// value stays empty.
const probeScript = `
LC_ALL=C; export LC_ALL
kv() { printf '%s=%s\n' "$1" "$2"; }
has() { command -v "$1" >/dev/null 2>&1; }
kv kernel "$(uname -s 2>/dev/null)"
kv arch "$(uname -m 2>/dev/null)"
if [ -r /etc/os-release ]; then
  kv os "$(sed -n 's/^PRETTY_NAME=//p' /etc/os-release | tr -d '"' | head -n1)"
  kv os_id "$(sed -n 's/^ID=//p' /etc/os-release | tr -d '"' | head -n1)"
fi
kv user "$(id -un 2>/dev/null)"
[ "$(id -u 2>/dev/null)" = 0 ] && kv root 1
has sudo && sudo -n true >/dev/null 2>&1 && kv sudo 1
if has docker; then
  kv docker 1
  kv docker_version "$(docker version --format '{{.Server.Version}}' 2>/dev/null)"
  kv compose "$(docker compose version --short 2>/dev/null)"
  kv projects "$(docker compose ls --quiet 2>/dev/null | wc -l | tr -d ' ')"
  kv containers "$(docker ps --quiet 2>/dev/null | wc -l | tr -d ' ')"
  state="$(docker info --format '{{.Swarm.LocalNodeState}} {{.Swarm.ControlAvailable}} {{.Swarm.Cluster.ID}}' 2>/dev/null)"
  case "$state" in
    "active true "*) kv swarm manager; kv swarm_cluster "${state##* }" ;;
    "active false"*) kv swarm worker ;;
  esac
  kv nepfleet_image "$(docker ps --format '{{.Image}}' 2>/dev/null | grep -m1 'nepfleet/nepfleet\|/nepfleet:\|^nepfleet:')"
fi
has nepfleet && kv nepfleet "$(nepfleet version 2>/dev/null | head -n1)"
if has curl; then
  kv nepfleet_api "$(curl -fsS -m 3 http://127.0.0.1:7480/api/v1/info 2>/dev/null | head -c 300)"
elif has wget; then
  kv nepfleet_api "$(wget -qO- -T 3 http://127.0.0.1:7480/api/v1/info 2>/dev/null | head -c 300)"
fi
for d in "$NEPFLEET_FLEET" "$HOME/fleet" /srv/nepfleet/fleet /srv/nepfleet /srv/fleet /opt/nepfleet/fleet; do
  if [ -n "$d" ] && [ -f "$d/fleet.yaml" ]; then kv fleet "$d"; break; fi
done
`
