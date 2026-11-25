// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Serraniel/neptide/pkg/snapshot"
)

// diskCheck reports free space under Docker's data directory.
func diskCheck(ctx context.Context, e Env, root string) Check {
	free, err := e.FreeBytes(root)
	if err != nil {
		// In a container (or without access to the path), ask Docker:
		// a helper container mounting the directory sees the host's file system.
		free, err = probeFree(ctx, e, root)
	}
	if err != nil {
		// Not knowing is not a problem of the host: no warning.
		return Check{"Disk", OK, "not checked: " + root + " is not visible here", "check free space on the host: df -h " + root}
	}
	gb := float64(free) / (1 << 30)
	c := Check{"Disk", OK, fmt.Sprintf("%.1f GiB free under %s", gb, root), ""}
	switch {
	case gb < 2:
		c.Status, c.Fix = Fail, "free space: images and volume snapshots need room"
	case gb < 10:
		c.Status, c.Fix = Warn, "less than 10 GiB free; pulls and backups may fill it"
	}
	return c
}

// probeFree runs df in a short-lived container that mounts root read-only.
// It uses an image that is already there (--pull never), so the check never
// downloads anything: NepFleet's own image when it runs in a container, the
// backup helper image otherwise.
func probeFree(ctx context.Context, e Env, root string) (uint64, error) {
	if !strings.HasPrefix(root, "/") {
		return 0, fmt.Errorf("unexpected Docker root %q", root)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var images []string
	if img := ownImage(ctx, e); img != "" {
		images = append(images, img)
	}
	images = append(images, snapshot.HelperImage)
	err := errors.New("no image to probe with")
	for _, img := range images {
		var out []byte
		out, err = e.Docker.Run(ctx, "", "run", "--rm", "--pull", "never", "--network", "none", "--log-driver", "none",
			"-v", root+":/probe:ro", "--entrypoint", "df", img, "-Pk", "/probe")
		if err != nil {
			continue
		}
		var free uint64
		if free, err = parseDF(out); err == nil {
			return free, nil
		}
	}
	return 0, err
}

// containerID finds this container's ID in the mounts Docker sets up for
// /etc/hostname and /etc/hosts. The hostname itself is often set by hand.
var containerID = regexp.MustCompile(`/containers/([0-9a-f]{64})/`)

// ownImage returns the image ID of the container nepfleet runs in, or "".
func ownImage(ctx context.Context, e Env) string {
	if !e.InContainer {
		return ""
	}
	raw, err := e.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	m := containerID.FindSubmatch(raw)
	if m == nil {
		return ""
	}
	out, err := e.Docker.Run(ctx, "", "inspect", "--format", "{{.Image}}", string(m[1]))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// parseDF reads the available bytes from POSIX df -k output.
func parseDF(out []byte) (uint64, error) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("unexpected df output %q", out)
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 4 {
		return 0, fmt.Errorf("unexpected df output %q", out)
	}
	kb, err := strconv.ParseUint(f[3], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unexpected df output %q", out)
	}
	return kb << 10, nil
}
