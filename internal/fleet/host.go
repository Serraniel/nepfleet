// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
}

// PortRange is an inclusive range of host ports.
type PortRange struct {
	From int `yaml:"from" schema:"required"`
	To   int `yaml:"to" schema:"required"`
}

// DefaultPorts is the range used when a host sets none.
var DefaultPorts = PortRange{From: 8000, To: 8999}

var hostAddrRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$|^\[[0-9a-fA-F:]+\]$`)

// LoadHost reads hosts/<name>.yaml; a host without a file has defaults
// (address: its name, ports: DefaultPorts).
func (f *Fleet) LoadHost(name string) (*Host, error) {
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
		return nil, fmt.Errorf("%s: spec.address %q is not a host name or IP address", path, h.Spec.Address)
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

// AccessPorts lists the LAN ports stacks on a target use (access mode port),
// by port, for conflict checks.
func (f *Fleet) AccessPorts(target string) (map[int][]string, error) {
	all, err := f.Stacks()
	if err != nil {
		return nil, err
	}
	out := map[int][]string{}
	for _, s := range all {
		if a := s.Spec.Access; a != nil && a.Mode == "port" && a.Port > 0 && strings.EqualFold(s.Spec.Target, target) {
			out[a.Port] = append(out[a.Port], s.Metadata.Name)
		}
	}
	return out, nil
}

// FreePort is the lowest port of the host's range no stack on it uses.
func (f *Fleet) FreePort(target string) (int, error) {
	h, err := f.LoadHost(target)
	if err != nil {
		return 0, err
	}
	used, err := f.AccessPorts(target)
	if err != nil {
		return 0, err
	}
	r := h.PortRange()
	for p := r.From; p <= r.To; p++ {
		if len(used[p]) == 0 {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port left in %d-%d on %s (hosts/%s.yaml spec.ports)", r.From, r.To, target, target)
}
