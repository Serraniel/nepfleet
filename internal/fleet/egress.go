// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"fmt"
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
}

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
	for _, h := range e.Block {
		if !hostRe.MatchString(h) {
			return fmt.Errorf("block: %q is not a host name (no wildcards: extra_hosts maps exact names)", h)
		}
	}
	return nil
}
