// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Hardware is what this host offers to device and gpu inputs.
type Hardware struct {
	// Serial lists USB serial devices by their stable /dev/serial/by-id path
	// (Zigbee/Thread sticks, Z-Wave, UPS).
	Serial []string
	// GPUs lists the gpu input values this host supports: intel, amd
	// (render nodes under /dev/dri) and nvidia (driver loaded).
	GPUs []string
}

// HostHardware looks at /dev and /sys (root is "/" except in tests).
func HostHardware(root string) Hardware {
	var hw Hardware
	if entries, err := os.ReadDir(filepath.Join(root, "dev/serial/by-id")); err == nil {
		for _, e := range entries {
			hw.Serial = append(hw.Serial, "/dev/serial/by-id/"+e.Name())
		}
	}
	vendors := map[string]string{"0x8086": "intel", "0x1002": "amd", "0x10de": "nvidia"}
	seen := map[string]bool{}
	nodes, _ := filepath.Glob(filepath.Join(root, "dev/dri/renderD*"))
	for _, n := range nodes {
		raw, err := os.ReadFile(filepath.Join(root, "sys/class/drm", filepath.Base(n), "device/vendor"))
		if err != nil {
			continue
		}
		if v, ok := vendors[strings.TrimSpace(string(raw))]; ok && v != "nvidia" {
			seen[v] = true // NVIDIA needs its container toolkit, not /dev/dri
		}
	}
	if _, err := os.Stat(filepath.Join(root, "dev/nvidia0")); err == nil {
		seen["nvidia"] = true
	}
	for g := range seen {
		hw.GPUs = append(hw.GPUs, g)
	}
	sort.Strings(hw.GPUs)
	return hw
}

// RenderGID is the group of the host's first /dev/dri render node, as a
// number ("" without one): containers need it in group_add to use the GPU
// as a non-root user, and group names differ between distributions.
func RenderGID(root string) string {
	nodes, _ := filepath.Glob(filepath.Join(root, "dev/dri/renderD*"))
	for _, n := range nodes {
		if fi, err := os.Stat(n); err == nil {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok {
				return strconv.FormatUint(uint64(st.Gid), 10)
			}
		}
	}
	return ""
}
