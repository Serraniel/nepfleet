// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Host is hosts/<name>.yaml: what NepFleet knows about one machine.
type Host struct {
	APIVersion string `yaml:"apiVersion" schema:"required;const=nepfleet/v1alpha1"`
	Kind       string `yaml:"kind" schema:"required;const=Host"`
	Metadata   struct {
		Name string `yaml:"name" schema:"required;pattern=^[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?$"`
	} `yaml:"metadata" schema:"required"`
	Spec HostSpec `yaml:"spec"`
}

// HostSpec describes a host.
type HostSpec struct {
	// Address is how devices on the LAN reach the host (a name such as
	// nas.local, or an IP), used in the URLs of stacks published on a port.
	Address string `yaml:"address"`
	// Ports is the range NepFleet picks LAN ports from for stacks with
	// access mode port.
	Ports *PortRange `yaml:"ports"`
	// Mounts are network shares the host mounts (nepfleet host apply).
	Mounts []HostMount `yaml:"mounts"`
	// Updates schedules OS updates of the host (see docs/host-updates.md).
	Updates *HostUpdates `yaml:"updates"`
}

// HostMount is a network share on a host.
type HostMount struct {
	Name string `yaml:"name" schema:"required;pattern=^[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?$"`
	Type string `yaml:"type" schema:"required;enum=nfs|cifs|sshfs"`
	// Source: host:/path (NFS), //host/share (SMB/CIFS), user@host:path (SSHFS).
	Source string `yaml:"source" schema:"required"`
	// Target is where it appears on the host.
	Target string `yaml:"target" schema:"required"`
	// User and PasswordSecret (a name in fleet.secret.yaml) log in to SMB.
	User           string   `yaml:"user"`
	PasswordSecret string   `yaml:"passwordSecret"`
	Options        []string `yaml:"options"`
}

var (
	mountNameRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]{0,61}[a-z0-9])?$`)
	mountTargetRe  = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)
	mountOptionRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_=.:-]*$`)
	mountUserRe    = regexp.MustCompile(`^[A-Za-z0-9._@-]+$`)
	mountSourceRes = map[string]*regexp.Regexp{
		"nfs":   regexp.MustCompile(`^[A-Za-z0-9.-]+:/[A-Za-z0-9._/-]*$`),
		"cifs":  regexp.MustCompile(`^//[A-Za-z0-9.-]+/[A-Za-z0-9._/-]+$`),
		"sshfs": regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._/-]*$`),
	}
)

// ValidMountName and ValidMountTarget check values before they reach a shell.
func ValidMountName(s string) bool { return mountNameRe.MatchString(s) }

// ValidMountTarget refuses paths a share must never cover.
func ValidMountTarget(s string) bool {
	if !mountTargetRe.MatchString(s) || strings.Contains(s, "/.") {
		return false
	}
	for _, sys := range []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root", "/run", "/sbin", "/sys", "/usr", "/var"} {
		if s == sys || strings.HasPrefix(s, sys+"/") {
			return false
		}
	}
	return true
}

func (m HostMount) validate() error {
	re, ok := mountSourceRes[m.Type]
	switch {
	case !ValidMountName(m.Name):
		return fmt.Errorf("mount %q: not a valid name", m.Name)
	case !ok:
		return fmt.Errorf("mount %s: type is nfs, cifs or sshfs", m.Name)
	case !re.MatchString(m.Source):
		return fmt.Errorf("mount %s: %q is not a %s source", m.Name, m.Source, m.Type)
	case !ValidMountTarget(m.Target):
		return fmt.Errorf("mount %s: %q can't be a target: an absolute path, not in a system folder", m.Name, m.Target)
	case m.Type == "cifs" && (!mountUserRe.MatchString(m.User) || m.PasswordSecret == ""):
		return fmt.Errorf("mount %s: SMB needs user and passwordSecret", m.Name)
	}
	for _, o := range m.Options {
		if !mountOptionRe.MatchString(o) {
			return fmt.Errorf("mount %s: option %q isn't allowed", m.Name, o)
		}
	}
	return nil
}

// HostUpdates configures unattended OS updates.
type HostUpdates struct {
	Enabled  bool   `yaml:"enabled"`
	Schedule string `yaml:"schedule"`
	Security string `yaml:"security"`
	Reboot   string `yaml:"reboot" schema:"enum=never|when-required|always"`
}

// PortRange is an inclusive range of host ports.
type PortRange struct {
	From int `yaml:"from" schema:"required"`
	To   int `yaml:"to" schema:"required"`
}

// DefaultPorts is the range used when a host sets none.
var DefaultPorts = PortRange{From: 8000, To: 8999}

// hostAddrRe: a host name or an IPv4 address (also what a DOMAIN input
// accepts, as port mode fills DOMAIN with it).
var hostAddrRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

var hostNameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_.-]{0,62})$`)

// LoadHost reads hosts/<name>.yaml; a host without a file has defaults
// (address: its name, ports: DefaultPorts).
func (f *Fleet) LoadHost(name string) (*Host, error) {
	if !hostNameRe.MatchString(name) {
		return nil, fmt.Errorf("%q is not a host name", name)
	}
	h := &Host{APIVersion: APIVersion, Kind: "Host"}
	h.Metadata.Name = name
	path := filepath.Join(f.Dir, "hosts", name+".yaml")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		h.Spec.Address = name
		return h, nil
	}
	if err := decodeStrict(path, h); err != nil {
		return nil, err
	}
	switch {
	case h.Kind != "Host" || h.APIVersion != APIVersion:
		return nil, fmt.Errorf("%s: want apiVersion %s and kind Host", path, APIVersion)
	case h.Metadata.Name != name:
		return nil, fmt.Errorf("%s: metadata.name must be %q", path, name)
	}
	if h.Spec.Address == "" {
		h.Spec.Address = name
	}
	if !hostAddrRe.MatchString(h.Spec.Address) {
		return nil, fmt.Errorf("%s: spec.address %q is not a host name or IPv4 address", path, h.Spec.Address)
	}
	seen := map[string]bool{}
	for _, m := range h.Spec.Mounts {
		if err := m.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if seen[m.Name] || seen[m.Target] {
			return nil, fmt.Errorf("%s: mount %s or its target is listed twice", path, m.Name)
		}
		seen[m.Name], seen[m.Target] = true, true
	}
	if u := h.Spec.Updates; u != nil && u.Reboot != "" && u.Reboot != "never" && u.Reboot != "when-required" && u.Reboot != "always" {
		return nil, fmt.Errorf("%s: spec.updates.reboot is never, when-required or always", path)
	}
	if r := h.Spec.Ports; r != nil && (r.From < 1024 || r.To > 65535 || r.From > r.To) {
		return nil, fmt.Errorf("%s: spec.ports must be a range within 1024-65535", path)
	}
	return h, nil
}

// PortRange is the host's range for LAN ports.
func (h *Host) PortRange() PortRange {
	if h.Spec.Ports != nil {
		return *h.Spec.Ports
	}
	return DefaultPorts
}

// AccessPorts lists the LAN ports stacks on a host use (access mode port),
// by port, for conflict checks. targets are the names that mean the host
// (its name, and "local" on the host itself).
func (f *Fleet) AccessPorts(targets ...string) (map[int][]string, error) {
	all, err := f.Stacks()
	if err != nil {
		return nil, err
	}
	out := map[int][]string{}
	for _, s := range all {
		if a := s.Spec.Access; a != nil && a.Mode == "port" && a.Port > 0 && slices.ContainsFunc(targets, func(t string) bool { return strings.EqualFold(s.Spec.Target, t) }) {
			out[a.Port] = append(out[a.Port], s.Metadata.Name)
		}
	}
	return out, nil
}

// FreePort is the lowest port of a host's range no stack on it uses.
// targets are the names stacks use for the host (see AccessPorts).
func (f *Fleet) FreePort(host string, targets ...string) (int, error) {
	h, err := f.LoadHost(host)
	if err != nil {
		return 0, err
	}
	used, err := f.AccessPorts(append(targets, host)...)
	if err != nil {
		return 0, err
	}
	r := h.PortRange()
	for p := r.From; p <= r.To; p++ {
		if len(used[p]) == 0 {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port left in %d-%d on %s (hosts/%s.yaml spec.ports)", r.From, r.To, host, host)
}
