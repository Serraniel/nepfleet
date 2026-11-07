// SPDX-License-Identifier: AGPL-3.0-or-later

// Package doctor checks that a host can run what the fleet plans for it.
package doctor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/Serraniel/nepfleet/internal/deploy"
)

// Status of one check.
type Status int

// Statuses, in increasing severity.
const (
	OK Status = iota
	Warn
	Fail
)

func (s Status) String() string { return [...]string{"✓", "⚠", "✗"}[s] }

// Check is one result.
type Check struct {
	Name   string
	Status Status
	Detail string
	Fix    string
}

// Env is what the checks may look at; tests replace parts of it.
type Env struct {
	Docker deploy.Runner
	// LookPath finds binaries (exec.LookPath).
	LookPath func(string) (string, error)
	// ReadFile reads host files (/proc, /sys).
	ReadFile func(string) ([]byte, error)
	// FreeBytes reports free space of a path.
	FreeBytes func(string) (uint64, error)
	// Command runs a host command (timedatectl, age-keygen).
	Command func(ctx context.Context, name string, args ...string) ([]byte, error)
	// InContainer is true when nepfleet itself runs in a container, where
	// the host's listeners and Docker's data directory are not visible.
	InContainer bool
	// BinfmtDir overrides where binfmt_misc is read (NEPFLEET_BINFMT_DIR).
	BinfmtDir string
}

// Host returns an Env backed by the real host.
func Host() Env {
	return Env{
		Docker:    deploy.Docker{},
		LookPath:  exec.LookPath,
		ReadFile:  os.ReadFile,
		FreeBytes: freeBytes,
		Command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
		InContainer: inContainer(),
		BinfmtDir:   os.Getenv("NEPFLEET_BINFMT_DIR"),
	}
}

// inContainer reports whether this process runs in a Docker or Podman container.
func inContainer() bool {
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

// Input is what the fleet expects of this host.
type Input struct {
	// Recipients are fleet.yaml's age public keys.
	Recipients []string
	// AgeKeyFile is the private key sops will use (SOPS_AGE_KEY_FILE or default).
	AgeKeyFile string
	// KeyFromEnv names SOPS_AGE_KEY or SOPS_AGE_KEY_CMD when one is set.
	KeyFromEnv string
	// Images planned for this host.
	Images []string
	// Ingress reports whether a stack publishes 80/443 through Caddy here.
	Ingress bool
	// LogRotation reports whether NepFleet sets rotating log options on
	// services (fleet.yaml spec.logging).
	LogRotation bool
}

// Run performs every check.
func Run(ctx context.Context, e Env, in Input) []Check {
	var out []Check
	add := func(c Check) { out = append(out, c) }

	// Docker and Compose.
	var info struct {
		ServerVersion string `json:"ServerVersion"`
		Architecture  string `json:"Architecture"`
		DockerRootDir string `json:"DockerRootDir"`
		LoggingDriver string `json:"LoggingDriver"`
		CgroupVersion string `json:"CgroupVersion"`
	}
	raw, err := e.Docker.Run(ctx, "", "info", "--format", "{{json .}}")
	if err == nil {
		err = json.Unmarshal(raw, &info)
	}
	if err != nil {
		add(Check{"Docker", Fail, err.Error(), "install Docker and make sure this user may use it (docker group or root)"})
		// Still report what does not need Docker.
		out = append(out, toolChecks(e)...)
		add(ageKey(ctx, e, in))
		return out
	}
	add(versionCheck("Docker", info.ServerVersion, 25, 0, "upgrade Docker to 25 or newer"))
	if v, err := e.Docker.Run(ctx, "", "compose", "version", "--short"); err != nil {
		add(Check{"Docker Compose", Fail, "the compose plugin is missing", "install docker-compose (the CLI plugin)"})
	} else {
		add(versionCheck("Docker Compose", strings.TrimSpace(string(v)), 2, 24, "upgrade the Compose plugin to 2.24 or newer"))
	}
	if info.CgroupVersion == "2" {
		add(Check{"cgroup", OK, "v2", ""})
	} else {
		add(Check{"cgroup", Warn, "v" + info.CgroupVersion, "cgroup v2 is the tested setup; v1 works but is deprecated"})
	}

	out = append(out, toolChecks(e)...)

	// Age key.
	add(ageKey(ctx, e, in))

	// Disk.
	if free, err := e.FreeBytes(info.DockerRootDir); err != nil {
		if e.InContainer {
			add(Check{"Disk", Warn, "cannot see " + info.DockerRootDir + " from inside the container", "check free space on the host: df -h " + info.DockerRootDir})
		} else {
			add(Check{"Disk", Warn, err.Error(), ""})
		}
	} else {
		gb := float64(free) / (1 << 30)
		c := Check{"Disk", OK, fmt.Sprintf("%.1f GiB free under %s", gb, info.DockerRootDir), ""}
		switch {
		case gb < 2:
			c.Status, c.Fix = Fail, "free space: images and volume snapshots need room"
		case gb < 10:
			c.Status, c.Fix = Warn, "less than 10 GiB free; pulls and backups may fill it"
		}
		add(c)
	}

	// Logs: json-file without options grows without bound.
	switch {
	case in.LogRotation:
		add(Check{"Log rotation", OK, "set by NepFleet on every service (fleet.yaml spec.logging)", ""})
	case info.LoggingDriver == "json-file" || info.LoggingDriver == "":
		add(Check{"Log rotation", Warn, "NepFleet's log defaults are off and the daemon logs to json-file, which grows without bound unless /etc/docker/daemon.json sets log-opts", "enable spec.logging in fleet.yaml (or check daemon.json's log-opts max-size)"})
	default:
		add(Check{"Log rotation", OK, "daemon log driver " + info.LoggingDriver, ""})
	}

	// Time.
	if v, err := e.Command(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value"); err == nil {
		if strings.TrimSpace(string(v)) == "yes" {
			add(Check{"Time sync", OK, "NTP synchronised", ""})
		} else {
			add(Check{"Time sync", Warn, "not synchronised", "enable systemd-timesyncd or chrony; TLS and TOTP depend on correct time"})
		}
	}

	// Ports.
	if in.Ingress {
		for _, port := range []int{80, 443} {
			add(portCheck(ctx, e, port))
		}
	}

	out = append(out, archChecks(ctx, e, hostPlatform(ctx, e, info.Architecture), in.Images)...)
	return out
}

func toolChecks(e Env) []Check {
	var out []Check
	for _, tool := range []struct{ name, fix string }{
		{"git", "install git"},
		{"sops", "install sops (secrets are encrypted with it)"},
		{"age-keygen", "install age"},
	} {
		if _, err := e.LookPath(tool.name); err != nil {
			out = append(out, Check{tool.name, Fail, "not found", tool.fix})
		} else {
			out = append(out, Check{tool.name, OK, "found", ""})
		}
	}
	return out
}

type platform struct{ arch, variant string }

func (p platform) String() string {
	if p.variant == "" {
		return p.arch
	}
	return p.arch + "/" + p.variant
}

// variant derives the ARM variant from the kernel architecture.
func variant(kernel, arch string) string {
	switch {
	case arch != "arm":
		return ""
	case kernel == "armv6l":
		return "v6"
	default:
		return "v7" // armv7l, or a 32-bit userland on aarch64
	}
}

// runs reports whether an image built for (arch, v) runs on host h: a v7
// host runs v6 images too, a v6 host only v6.
func (p platform) runs(arch, v string) bool {
	if normaliseArch(arch) != p.arch {
		return false
	}
	if p.arch != "arm" {
		return true
	}
	switch p.variant {
	case "v6":
		return v == "v6"
	default:
		return v == "" || v == "v6" || v == "v7"
	}
}

// Failed reports whether any check failed.
func Failed(cs []Check) bool {
	for _, c := range cs {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

func versionCheck(name, version string, major, minor int, fix string) Check {
	parts := strings.SplitN(strings.TrimPrefix(version, "v"), ".", 3)
	maj, _ := strconv.Atoi(parts[0])
	mnr := 0
	if len(parts) > 1 {
		mnr, _ = strconv.Atoi(parts[1])
	}
	if maj > major || (maj == major && mnr >= minor) {
		return Check{name, OK, version, ""}
	}
	return Check{name, Fail, version, fix}
}

func ageKey(ctx context.Context, e Env, in Input) Check {
	if len(in.Recipients) == 0 {
		return Check{"age key", Fail, "fleet.yaml has no spec.secrets.age recipients", "age-keygen -o key.txt, then add the public key to fleet.yaml"}
	}
	if in.KeyFromEnv != "" {
		return Check{"age key", Warn, "provided through " + in.KeyFromEnv + "; not verified against the recipients", ""}
	}
	if _, err := e.ReadFile(in.AgeKeyFile); err != nil {
		return Check{"age key", Fail, "no private key at " + in.AgeKeyFile, "copy the fleet's age key there or set SOPS_AGE_KEY_FILE"}
	}
	pub, err := e.Command(ctx, "age-keygen", "-y", in.AgeKeyFile)
	if err != nil {
		return Check{"age key", Fail, "cannot read " + in.AgeKeyFile, "check the file"}
	}
	for _, line := range strings.Fields(string(pub)) {
		for _, r := range in.Recipients {
			if line == r {
				return Check{"age key", OK, "matches a fleet recipient; keep a copy off this host", ""}
			}
		}
	}
	return Check{"age key", Fail, "the key in " + in.AgeKeyFile + " is not a recipient in fleet.yaml", "use the fleet's key, or add this public key and re-encrypt (sops updatekeys)"}
}

// portCheck looks for listeners in /proc/net/tcp{,6} and names a container
// publishing the port, if any.
func portCheck(ctx context.Context, e Env, port int) Check {
	name := fmt.Sprintf("Port %d", port)
	listening := false
	// In a container /proc/net shows the container's own network namespace,
	// so only Docker can tell whether the host port is taken.
	files := []string{"/proc/net/tcp", "/proc/net/tcp6"}
	if e.InContainer {
		files, listening = nil, true
	}
	for _, f := range files {
		raw, err := e.ReadFile(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(strings.NewReader(string(raw)))
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 4 || fields[3] != "0A" { // 0A = LISTEN
				continue
			}
			_, hexPort, ok := strings.Cut(fields[1], ":")
			if p, err := strconv.ParseInt(hexPort, 16, 32); ok && err == nil && int(p) == port {
				listening = true
			}
		}
	}
	if !listening {
		return Check{name, OK, "free", ""}
	}
	out, _ := e.Docker.Run(ctx, "", "ps", "--filter", fmt.Sprintf("publish=%d", port), "--format", "{{.Names}} ({{.Image}})")
	who := strings.TrimSpace(string(out))
	if strings.Contains(who, "caddy") {
		return Check{name, OK, "used by " + who, ""}
	}
	if who == "" && e.InContainer {
		return Check{name, Warn, "no container publishes it; host processes are not visible from inside a container", fmt.Sprintf("check on the host: ss -ltn sport = :%d", port)}
	}
	if who == "" {
		who = "a process on the host"
	}
	return Check{name, Fail, "in use by " + who, "stop it, or import that stack so Caddy can take over"}
}

func normaliseArch(a string) string {
	switch a {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "armv7l", "armv6l", "armhf", "arm":
		return "arm"
	}
	return a
}
