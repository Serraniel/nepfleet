// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/render"
)

// bindSources are the host paths a compose file bind-mounts.
func bindSources(compose []byte, env map[string]string) []string {
	var doc struct {
		Services map[string]struct {
			Volumes []any `yaml:"volumes"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal(compose, &doc) != nil {
		return nil
	}
	pairs := make([]string, 0, 2*len(env))
	for k, v := range env {
		pairs = append(pairs, "${"+k+"}", v)
	}
	expand := strings.NewReplacer(pairs...)
	var out []string
	for _, s := range doc.Services {
		for _, v := range s.Volumes {
			var src string
			switch vol := v.(type) {
			case string:
				src, _, _ = strings.Cut(expand.Replace(vol), ":")
			case map[string]any:
				if t, _ := vol["type"].(string); t == "bind" {
					s, _ := vol["source"].(string)
					src = expand.Replace(s)
				}
			}
			if strings.HasPrefix(src, "/") {
				out = append(out, src)
			}
		}
	}
	return out
}

// mountsNeeded are the host's shares that these paths lie on.
func mountsNeeded(mounts []fleet.HostMount, sources []string) []fleet.HostMount {
	var out []fleet.HostMount
	for _, m := range mounts {
		for _, s := range sources {
			if s == m.Target || strings.HasPrefix(s, m.Target+"/") {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// isMounted reads the kernel's table. An automount alone doesn't count: that is
// only the trigger, and touching the path first gives it its chance.
func isMounted(mountinfo, target string) bool {
	for _, line := range strings.Split(mountinfo, "\n") {
		left, right, ok := strings.Cut(line, " - ")
		f := strings.Fields(left)
		if !ok || len(f) < 5 || f[4] != target {
			continue
		}
		if t := strings.Fields(right); len(t) > 0 && t[0] != "autofs" {
			return true
		}
	}
	return false
}

// checkMounts refuses a stack whose share is missing: without this, a container
// would silently write into the empty local folder underneath.
func (e *env) checkMounts(p *prepared) error {
	h, err := e.fleet.LoadHost(e.targetHost(p.stack.Spec.Target))
	if err != nil || len(h.Spec.Mounts) == 0 {
		return nil //nolint:nilerr // a host without a valid file declares no mounts; port mode reports the file itself
	}
	needed := mountsNeeded(h.Spec.Mounts, bindSources(p.out.Files[render.ComposeFile], render.EnvValues(p.out.Files[render.EnvFile])))
	if len(needed) == 0 {
		return nil
	}
	// In a container, the host's mounts aren't visible in this mount table.
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return nil
	}
	for _, m := range needed {
		_, _ = os.ReadDir(m.Target) // triggers an automount
	}
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil //nolint:nilerr // no such table on this system: nothing to go by
	}
	for _, m := range needed {
		if !isMounted(string(raw), m.Target) {
			return fmt.Errorf("%s: the share %s isn't mounted at %s, so its containers would write into the empty folder; check it, or run nepfleet host apply", p.stack.Metadata.Name, m.Name, m.Target)
		}
	}
	return nil
}
