// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/Serraniel/nepfleet/internal/render"
)

// apiURLFor is the API on render.APINetwork, with the port serve listens on.
func apiURLFor(listen string) string {
	if _, port, err := net.SplitHostPort(listen); err == nil && port != "" {
		return "http://nepfleet:" + port
	}
	return render.DefaultAPIURL
}

// containerIDRe finds this container's ID in the mount of its /etc/hostname,
// which Docker bind-mounts from the container's own directory.
var containerIDRe = regexp.MustCompile(`/containers/([0-9a-f]{64})/hostname$`)

// ownContainer is the ID of the container this process runs in, or "".
func ownContainer() string {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	return containerFromMounts(string(raw))
}

// containerFromMounts reads /proc/self/mountinfo: only the mount on
// /etc/hostname counts, never another container's directory mounted in.
func containerFromMounts(mountinfo string) string {
	for _, line := range strings.Split(mountinfo, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[4] != "/etc/hostname" {
			continue
		}
		if m := containerIDRe.FindStringSubmatch(f[3]); m != nil {
			return m[1]
		}
	}
	return ""
}

// joinAPINetwork puts the server's container on render.APINetwork, as
// "nepfleet", so stacks that use the API (log alerts, widgets) reach it
// there. The network is internal: it connects them to the API and nothing
// else. Done at start, since a self-update replaces only the image.
func joinAPINetwork(ctx context.Context, listen string, stderr io.Writer) {
	id := ownContainer()
	if id == "" {
		return // not in a container: stacks reach the API some other way
	}
	if host, _, err := net.SplitHostPort(listen); err == nil {
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			fmt.Fprintf(stderr, "nepfleet: listening on %s, so stacks on %s can't reach the API; listen on :<port> inside the container\n", listen, render.APINetwork)
		}
	}
	docker := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput() //nolint:gosec // fixed arguments, a 64-hex container ID
		return strings.TrimSpace(string(out)), err
	}
	if _, err := docker("network", "inspect", render.APINetwork); err != nil {
		args := []string{"network", "create", "--internal"}
		if state, _ := docker("info", "--format", "{{.Swarm.LocalNodeState}}"); state == "active" {
			args = append(args, "--driver", "overlay", "--attachable")
		}
		if out, err := docker(append(args, render.APINetwork)...); err != nil && !strings.Contains(out, "already exists") {
			fmt.Fprintf(stderr, "nepfleet: creating network %s for stacks that use the API: %s\n", render.APINetwork, firstLine(out))
			return
		}
	}
	if out, err := docker("network", "connect", "--alias", "nepfleet", render.APINetwork, id); err != nil && !strings.Contains(out, "already exists") {
		fmt.Fprintf(stderr, "nepfleet: joining network %s: %s\n", render.APINetwork, firstLine(out))
	}
}

// firstLine keeps Docker's error to one line, for the log.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
