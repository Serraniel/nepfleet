// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
)

// TelemetryHosts are endpoints that software in containers reports usage to
// without asking. Each is resolved to 0.0.0.0 in every service, so the
// connection fails at once; nothing else is affected. Only hosts that are
// used for telemetry alone belong here (never update or package servers).
var TelemetryHosts = []string{
	// onnxruntime (e.g. in Immich's machine learning): 1DS telemetry to
	// Microsoft, on by default since 1.29 (docs/Privacy.md).
	"mobile.events.data.microsoft.com",
	// Scarf download and usage analytics, used by many images.
	"static.scarf.sh",
	"scarf.sh",
	// PostHog product analytics (Stirling-PDF, n8n and others).
	"app.posthog.com",
	"us.i.posthog.com",
	"eu.i.posthog.com",
	// Syncthing usage and crash reports.
	"data.syncthing.net",
	"crash.syncthing.net",
}

// Egress limits what a stack's containers reach on the internet.
type Egress struct {
	// BlockTelemetry resolves the known telemetry hosts (TelemetryHosts)
	// to 0.0.0.0 in every service; on unless set to false.
	BlockTelemetry *bool `yaml:"blockTelemetry"`
	// Block lists more host names to resolve to 0.0.0.0.
	Block []string `yaml:"block"`
	// Mode is off (the default) or enforce: containers reach only private
	// networks and what is allowed. A stack's mode replaces the fleet's.
	Mode string `yaml:"mode" schema:"enum=off|enforce"`
	// Allow lists host names or addresses (or CIDR ranges) containers may
	// reach in mode enforce. Both lists count.
	Allow []string `yaml:"allow"`
}

// Egress modes.
const (
	EgressOff     = "off"
	EgressEnforce = "enforce"
)

// EffectiveEgress is a stack's mode, and everything the fleet and it allow.
func EffectiveEgress(f *Fleet, s *Stack) (mode string, allow []string) {
	mode = EgressOff
	for _, e := range []*Egress{fleetEgress(f), stackEgress(s)} {
		if e == nil {
			continue
		}
		if e.Mode != "" {
			mode = e.Mode
		}
		allow = append(allow, e.Allow...)
	}
	return mode, allow
}

// ValidHost says whether s is a plain host name, safe to hand to a resolver.
func ValidHost(s string) bool { return hostRe.MatchString(s) }

// BlockedHosts is what a stack's services must not reach: the telemetry
// list unless the fleet or the stack turns it off, plus both Block lists.
func BlockedHosts(f *Fleet, s *Stack) []string {
	telemetry := true
	var out []string
	for _, e := range []*Egress{fleetEgress(f), stackEgress(s)} {
		if e == nil {
			continue
		}
		if e.BlockTelemetry != nil {
			telemetry = *e.BlockTelemetry
		}
		out = append(out, e.Block...)
	}
	if telemetry {
		out = append(slices.Clone(TelemetryHosts), out...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func fleetEgress(f *Fleet) *Egress {
	if f == nil {
		return nil
	}
	return f.Spec.Egress
}

func stackEgress(s *Stack) *Egress {
	if s == nil {
		return nil
	}
	return s.Spec.Egress
}

var hostRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

func checkEgress(e *Egress) error {
	if e == nil {
		return nil
	}
	switch e.Mode {
	case "", EgressOff, EgressEnforce:
	default:
		return fmt.Errorf("mode: %q is not off or enforce", e.Mode)
	}
	for _, a := range e.Allow {
		_, aerr := netip.ParseAddr(a)
		_, perr := netip.ParsePrefix(a)
		if aerr != nil && perr != nil && !hostRe.MatchString(a) {
			return fmt.Errorf("allow: %q is not a host name or address (no wildcards)", a)
		}
	}
	for _, h := range e.Block {
		if !hostRe.MatchString(h) {
			return fmt.Errorf("block: %q is not a host name (no wildcards: extra_hosts maps exact names)", h)
		}
	}
	return nil
}
