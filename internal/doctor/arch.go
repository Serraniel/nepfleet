// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// hostPlatform is the platform the daemon pulls for: a 32-bit userland on a
// 64-bit kernel pulls arm/v7, although the kernel says aarch64.
func hostPlatform(ctx context.Context, e Env, kernelArch string) platform {
	arch := normaliseArch(kernelArch)
	if v, err := e.Docker.Run(ctx, "", "version", "--format", "{{.Server.Arch}}"); err == nil && strings.TrimSpace(string(v)) != "" {
		arch = normaliseArch(strings.TrimSpace(string(v)))
	}
	return platform{arch, variant(kernelArch, arch)}
}

// ImageArch is the verdict for one image on this host.
type ImageArch struct {
	Image string
	// Status is OK when the image runs natively, Warn when it runs only under
	// binfmt emulation or could not be checked, Fail when it cannot run.
	Status Status
	Detail string
}

// Architectures checks whether each image can run on this host, natively
// or through a registered binfmt emulator (the system/binfmt template).
// Images that cannot be checked (private registry, single-platform) get Warn.
func Architectures(ctx context.Context, e Env, images []string) []ImageArch {
	raw, err := e.Docker.Run(ctx, "", "info", "--format", "{{.Architecture}}")
	if err != nil {
		return nil
	}
	return imageArchs(ctx, e, hostPlatform(ctx, e, strings.TrimSpace(string(raw))), images)
}

func imageArchs(ctx context.Context, e Env, host platform, images []string) []ImageArch {
	var out []ImageArch
	for _, img := range images {
		plats, known := imagePlatforms(ctx, e, img)
		if !known {
			out = append(out, ImageArch{img, Warn, "could not verify: private registry without credentials, or a single-platform image"})
			continue
		}
		var emulated, thirtyTwo string
		native := false
		for _, p := range plats {
			if host.runs(p.arch, p.variant) {
				native = true
				break
			}
			if emulated == "" && e.emulates(p.arch) {
				emulated = p.String()
			}
			if thirtyTwo == "" && compat(host, p) {
				thirtyTwo = p.String()
			}
		}
		switch {
		case native:
			out = append(out, ImageArch{img, OK, "built for " + host.String()})
		case emulated != "":
			out = append(out, ImageArch{img, Warn, fmt.Sprintf("not built for %s; runs as %s under binfmt emulation (slower)", host, emulated)})
		case thirtyTwo != "":
			out = append(out, ImageArch{img, Warn, fmt.Sprintf("only a 32-bit %s build; runs if this CPU supports 32-bit mode", thirtyTwo)})
		case !e.binfmtVisible() && e.InContainer:
			out = append(out, ImageArch{img, Warn, fmt.Sprintf("not built for %s (only %s), and binfmt_misc is not visible from the container to check for emulation; mount it as in compose.yaml", host, list(plats))})
		default:
			out = append(out, ImageArch{img, Fail, fmt.Sprintf("not built for %s (only %s)", host, list(plats))})
		}
	}
	return out
}

// imagePlatforms lists the linux platforms img is available for: the local
// image if it is present (that is what would run), else the registry index.
func imagePlatforms(ctx context.Context, e Env, img string) ([]platform, bool) {
	if raw, err := e.Docker.Run(ctx, "", "image", "inspect", "--format", "{{.Os}} {{.Architecture}} {{.Variant}}", img); err == nil {
		if f := strings.Fields(string(raw)); len(f) >= 2 && f[0] == "linux" {
			p := platform{arch: normaliseArch(f[1])}
			if len(f) > 2 {
				p.variant = f[2]
			}
			return []platform{p}, true
		}
	}
	ictx, cancel := context.WithTimeout(ctx, 20*time.Second)
	raw, err := e.Docker.Run(ictx, "", "manifest", "inspect", img)
	cancel()
	if err != nil {
		return nil, false
	}
	var m struct {
		Manifests []struct {
			Platform struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
				Variant      string `json:"variant"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if json.Unmarshal(raw, &m) != nil || len(m.Manifests) == 0 {
		return nil, false
	}
	var out []platform
	for _, mf := range m.Manifests {
		if mf.Platform.OS == "linux" && mf.Platform.Architecture != "unknown" { // "unknown" marks attestations
			out = append(out, platform{normaliseArch(mf.Platform.Architecture), mf.Platform.Variant})
		}
	}
	return out, len(out) > 0
}

// qemuNames maps Docker architectures to binfmt_misc entries.
var qemuNames = map[string]string{
	"amd64": "x86_64", "arm64": "aarch64", "arm": "arm", "386": "i386",
	"riscv64": "riscv64", "ppc64le": "ppc64le", "s390x": "s390x",
}

// emulates reports whether a binfmt_misc emulator for arch is registered
// and enabled. binfmt_misc is global to the kernel, so this also works for
// a NepFleet running in a container, if /proc/sys/fs/binfmt_misc is visible.
func (e Env) emulates(arch string) bool {
	name, ok := qemuNames[arch]
	if !ok || !e.binfmtVisible() {
		return false
	}
	raw, err := e.ReadFile(e.binfmtDir() + "/qemu-" + name)
	return err == nil && strings.HasPrefix(string(raw), "enabled")
}

// binfmtDir is where binfmt_misc is mounted. A container has its own
// /proc without it, so compose.yaml mounts the host's at /host/binfmt_misc
// (NEPFLEET_BINFMT_DIR).
func (e Env) binfmtDir() string {
	if e.BinfmtDir != "" {
		return e.BinfmtDir
	}
	return "/proc/sys/fs/binfmt_misc"
}

// binfmtVisible reports whether binfmt_misc can be read and is enabled.
func (e Env) binfmtVisible() bool {
	raw, err := e.ReadFile(e.binfmtDir() + "/status")
	return err == nil && strings.HasPrefix(string(raw), "enabled")
}

// compat reports images 64-bit hosts usually run natively in 32-bit mode
// (arm on arm64, 386 on amd64), which depends on the CPU.
func compat(host platform, p platform) bool {
	return (host.arch == "arm64" && p.arch == "arm") || (host.arch == "amd64" && p.arch == "386")
}

func archChecks(ctx context.Context, e Env, host platform, images []string) []Check {
	if len(images) == 0 {
		return nil
	}
	var out []Check
	var ok int
	var emulated, missing, unknown []string
	for _, a := range imageArchs(ctx, e, host, images) {
		switch {
		case a.Status == OK:
			ok++
		case a.Status == Fail:
			missing = append(missing, a.Image)
		case strings.HasPrefix(a.Detail, "could not verify"):
			unknown = append(unknown, a.Image)
		case strings.Contains(a.Detail, "under binfmt emulation"):
			emulated = append(emulated, a.Image)
		default:
			ok++
			out = append(out, Check{"Image architectures", Warn, a.Image + ": " + a.Detail, ""})
		}
	}
	if len(missing) > 0 {
		out = append(out, Check{"Image architectures", Fail, fmt.Sprintf("not built for %s: %s", host, strings.Join(missing, ", ")),
			"use another image, or enable emulation with the system/binfmt template (slower)"})
	} else {
		out = append(out, Check{"Image architectures", OK, fmt.Sprintf("%d image(s) available for %s", ok+len(emulated), host), ""})
	}
	if len(emulated) > 0 {
		out = append(out, Check{"Image architectures", Warn, "under binfmt emulation (slower): " + strings.Join(emulated, ", "), ""})
	}
	if len(unknown) > 0 {
		out = append(out, Check{"Image architectures", Warn, "could not verify: " + strings.Join(unknown, ", "), "private registry without credentials, or a single-platform image"})
	}
	return out
}

func list(ps []platform) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.String()
	}
	return strings.Join(s, ", ")
}
