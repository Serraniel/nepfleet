// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Serraniel/nepfleet/internal/render"
)

// Published is a host port a rendered stack asks for.
type Published struct {
	IP    string // empty: every address
	Port  int
	Proto string // tcp or udp
}

func (p Published) clashes(o Published) bool {
	return p.Port == o.Port && p.Proto == o.Proto && (p.IP == "" || o.IP == "" || p.IP == o.IP)
}

// maxPortRange bounds what one ports: entry can expand to.
const maxPortRange = 4096

var shortPortRe = regexp.MustCompile(`^(?:(\[[^\]]+\]|[0-9.]+):)?(?:(\d+)(?:-(\d+))?:)?\d+(?:-\d+)?(?:/(tcp|udp))?$`)

func wildcard(ip string) string {
	ip = strings.Trim(ip, "[]")
	if ip == "0.0.0.0" || ip == "::" {
		return ""
	}
	return ip
}

func portsIn(from, to, ip, proto string) []Published {
	first, err := strconv.Atoi(from)
	if err != nil || first <= 0 {
		return nil // not fixed: Docker picks a free one
	}
	last := first
	if to != "" {
		if last, err = strconv.Atoi(to); err != nil || last < first || last-first > maxPortRange {
			last = first
		}
	}
	if proto == "" {
		proto = "tcp"
	}
	out := make([]Published, 0, last-first+1)
	for p := first; p <= last; p++ {
		out = append(out, Published{IP: wildcard(ip), Port: p, Proto: proto})
	}
	return out
}

func parsePort(entry any) []Published {
	switch v := entry.(type) {
	case string:
		m := shortPortRe.FindStringSubmatch(v)
		if m == nil {
			return nil
		}
		return portsIn(m[2], m[3], m[1], m[4])
	case int:
		return nil // only a container port
	case map[string]any:
		if v["published"] == nil {
			return nil
		}
		from, to, _ := strings.Cut(fmt.Sprint(v["published"]), "-")
		var ip, proto string
		if s, ok := v["host_ip"].(string); ok {
			ip = s
		}
		if s, ok := v["protocol"].(string); ok {
			proto = s
		}
		return portsIn(from, to, ip, proto)
	}
	return nil
}

// PublishedPorts lists the host ports a rendered stack publishes.
func PublishedPorts(out *render.Output) []Published {
	var all []Published
	for _, svc := range services(out.Files[render.ComposeFile], out.Files[render.EnvFile]) {
		m, ok := svc.(map[string]any)
		if !ok {
			continue
		}
		ports, ok := m["ports"].([]any)
		if !ok {
			continue
		}
		for _, entry := range ports {
			all = append(all, parsePort(entry)...)
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Port != all[j].Port {
			return all[i].Port < all[j].Port
		}
		return all[i].Proto+all[i].IP < all[j].Proto+all[j].IP
	})
	return all
}

// PortClash is a port two stacks on one host both want.
type PortClash struct {
	Port         int
	First, Other string
}

// PortClashes finds ports stacks on one host would fight over.
func PortClashes(byStack map[string][]Published) []PortClash {
	names := make([]string, 0, len(byStack))
	for n := range byStack {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []PortClash
	seen := map[PortClash]bool{}
	for i, a := range names {
		for _, b := range names[i+1:] {
			for _, x := range byStack[a] {
				for _, y := range byStack[b] {
					c := PortClash{x.Port, a, b}
					if x.clashes(y) && !seen[c] {
						seen[c] = true
						out = append(out, c)
					}
				}
			}
		}
	}
	return out
}

// PortBusy says whether something on this machine holds a TCP port already.
func PortBusy(p Published) bool {
	if p.Proto != "tcp" {
		return false // binding UDP says little without sending
	}
	l, err := net.Listen("tcp", net.JoinHostPort(p.IP, strconv.Itoa(p.Port)))
	if err != nil {
		return true
	}
	_ = l.Close()
	return false
}
