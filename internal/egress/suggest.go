// SPDX-License-Identifier: AGPL-3.0-or-later

// Package egress reads what sidecars in egress mode log recorded, and turns it into
// an allowlist to review.
package egress

import (
	"bufio"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Report is where a stack's containers went.
type Report struct {
	// Hosts are names it looked up and then connected to.
	Hosts []string
	// LookedUp are names it looked up without connecting to their addresses.
	LookedUp []string
	// Addresses were connected to with no name known for them.
	Addresses []string
}

var (
	// A DNS answer, as tcpdump prints it with -v:
	//   127.0.0.11.53 > 127.0.0.1.51595: 3178 2/0/0 one.one.one.one. A 1.0.0.1, one.one.one.one. A 1.1.1.1 (65)
	// Failures show the question instead: NXDomain q: A? missing.example.org. 0/1/0
	dnsLineRe  = regexp.MustCompile(`\.53 > \S+: `)
	questionRe = regexp.MustCompile(`q: (?:A|AAAA)\? ([A-Za-z0-9._-]+?)\.? `)
	answerRe   = regexp.MustCompile(`(?:^| )([A-Za-z0-9._-]+)\. (A|AAAA|CNAME) ([0-9A-Za-z:._-]+)`)
	// "> 1.0.0.1.443: Flags [S]" or "> 2606:4700::1111.443: Flags [S]".
	connectRe = regexp.MustCompile(`> ([0-9a-fA-F:.]+)\.\d+: (?:Flags \[S|UDP|\d)`)
)

// public says whether an address is out on the internet, not private or local.
func public(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast()
}

// Parse reads the logs of a stack's sidecars. Lines it doesn't know are skipped: the
// text comes from containers' traffic, and only what's clearly an address or a name
// counts.
func Parse(logs string) Report {
	namesOf := map[netip.Addr]map[string]bool{}
	looked := map[string]bool{}
	connected := map[netip.Addr]bool{}
	sc := bufio.NewScanner(strings.NewReader(logs))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if dnsLineRe.MatchString(line) {
			answers := answerRe.FindAllStringSubmatch(line, -1)
			// What was asked: the question if shown, or the first answer's owner,
			// which is the name before any CNAME leads elsewhere.
			name := ""
			if q := questionRe.FindStringSubmatch(line); q != nil {
				name = q[1]
			} else if len(answers) > 0 {
				name = answers[0][1]
			}
			name = strings.ToLower(strings.TrimSuffix(name, "."))
			// One word is a service in the stack, and .local stays on the LAN.
			if !strings.Contains(name, ".") || strings.HasSuffix(name, ".local") {
				continue
			}
			looked[name] = true
			for _, a := range answers {
				if a[2] == "CNAME" {
					continue
				}
				if ip, err := netip.ParseAddr(a[3]); err == nil && public(ip) {
					if namesOf[ip] == nil {
						namesOf[ip] = map[string]bool{}
					}
					namesOf[ip][name] = true
				}
			}
			continue
		}
		if m := connectRe.FindStringSubmatch(line); m != nil {
			if ip, err := netip.ParseAddr(m[1]); err == nil && public(ip) {
				connected[ip] = true
			}
		}
	}
	hosts := map[string]bool{}
	var unnamed []string
	for ip := range connected {
		names := namesOf[ip]
		if len(names) == 0 {
			unnamed = append(unnamed, ip.String())
			continue
		}
		for n := range names {
			hosts[n] = true
		}
	}
	r := Report{Addresses: unnamed}
	for n := range hosts {
		r.Hosts = append(r.Hosts, n)
	}
	for n := range looked {
		if !hosts[n] {
			r.LookedUp = append(r.LookedUp, n)
		}
	}
	sort.Strings(r.Hosts)
	sort.Strings(r.LookedUp)
	sort.Strings(r.Addresses)
	return r
}

// Without takes away what's already allowed. An address counts as allowed inside a
// CIDR range too.
func (r Report) Without(allowed []string) Report {
	var ranges []netip.Prefix
	for _, a := range allowed {
		if p, err := netip.ParsePrefix(a); err == nil {
			ranges = append(ranges, p)
		} else if ip, err := netip.ParseAddr(a); err == nil {
			ranges = append(ranges, netip.PrefixFrom(ip, ip.BitLen()))
		}
	}
	keep := func(in []string, drop func(string) bool) []string {
		var out []string
		for _, x := range in {
			if !drop(x) {
				out = append(out, x)
			}
		}
		return out
	}
	named := func(s string) bool { return slices.Contains(allowed, s) }
	return Report{
		Hosts:    keep(r.Hosts, named),
		LookedUp: keep(r.LookedUp, named),
		Addresses: keep(r.Addresses, func(s string) bool {
			ip, err := netip.ParseAddr(s)
			return err == nil && slices.ContainsFunc(ranges, func(p netip.Prefix) bool { return p.Contains(ip) })
		}),
	}
}
