// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"bytes"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/fleet"
	"github.com/Serraniel/nepfleet/internal/template"
)

// egressImage holds iptables and dig; lock.yaml pins it like any other image.
const egressImage = "nicolaka/netshoot:v0.16"

// egressScript sets the rules in the network namespace it owns, then keeps them
// current, as hosts change address. A new chain is built first and swapped in, so
// a refresh never leaves a moment without rules; any failed rule fails the whole
// build. A failed lookup keeps that host's last addresses. Containers get private
// networks: their own stack, the ingress and the LAN. $$ is compose's escape for $.
const egressScript = `set -uf
build() { # command, chain, version
  $$1 -N "$$2" 2>/dev/null; $$1 -F "$$2" || return 1
  $$1 -A "$$2" -o lo -j ACCEPT || return 1
  $$1 -A "$$2" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT || return 1
  if [ "$$3" = 4 ]; then
    list="10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 169.254.0.0/16 $$NEPFLEET_EGRESS_V4"; type=A
  else
    list="fc00::/7 fe80::/10 $$NEPFLEET_EGRESS_V6"; type=AAAA
  fi
  for net in $$list; do $$1 -A "$$2" -d "$$net" -j ACCEPT || return 1; done
  for host in $$NEPFLEET_EGRESS_HOSTS; do
    cache="/run/nepfleet-egress-$$host-$$type"
    addrs="$$(dig +short +time=3 +tries=2 "$$host" "$$type" | grep -E '^[0-9a-fA-F:.]*[0-9a-fA-F:]$$')"
    if [ -n "$$addrs" ]; then echo "$$addrs" > "$$cache"; elif [ -f "$$cache" ]; then addrs="$$(cat "$$cache")"; fi
    for a in $$addrs; do $$1 -A "$$2" -d "$$a" -j ACCEPT || return 1; done
  done
  # Denials to the log, for nepfleet egress suggest; limited, and optional: a
  # kernel without NFLOG still enforces.
  $$1 -A "$$2" -m limit --limit 20/min --limit-burst 40 -j NFLOG --nflog-group 27 2>/dev/null || :
  $$1 -A "$$2" -j REJECT || return 1
}
# What was refused, and the names looked up, so a denial can be named (DNS
# answers on loopback: Docker's resolver rewrites the questions' ports).
tcpdump -l -n -p -v -i lo -Z root -s 512 'udp and src port 53' 2>/dev/null &
tcpdump -l -n -p -i nflog:27 -Z root -s 128 2>/dev/null &
apply() { # command, version
  if $$1 -S NEPFLEET_A >/dev/null 2>&1; then new=NEPFLEET_B; old=NEPFLEET_A; else new=NEPFLEET_A; old=NEPFLEET_B; fi
  build "$$1" "$$new" "$$2" || return 1
  if $$1 -S "$$old" >/dev/null 2>&1; then
    $$1 -R OUTPUT 1 -j "$$new" && $$1 -F "$$old" && $$1 -X "$$old"
  else
    $$1 -I OUTPUT 1 -j "$$new"
  fi
}
has_v6() { ip -6 addr show scope global | grep -q inet6; }
apply iptables 4 || exit 1
if ! apply ip6tables 6; then
  if has_v6; then echo "no IPv6 rules on a network with IPv6" >&2; exit 1; fi
fi
touch /run/nepfleet-egress-ready
while :; do
  sleep 300
  apply iptables 4 || echo "refresh failed; the last rules stay" >&2
  apply ip6tables 6 >/dev/null 2>&1
done
`

// EgressAllow is everything a stack may reach: the fleet's and stack's lists,
// what its template declares, and the hosts of URLs given as its values.
func EgressAllow(allow []string, t *template.Template, env map[string]string) []string {
	out := slices.Clone(allow)
	if t != nil {
		for _, e := range t.Spec.Egress {
			out = append(out, e.Host)
		}
		for _, inp := range t.Spec.Inputs {
			if inp.Type != "url" {
				continue
			}
			if u, err := url.Parse(env[inp.Name]); err == nil && u.Hostname() != "" {
				out = append(out, u.Hostname())
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// splitAllow sorts destinations for the script; they reach a shell, so anything
// else is refused here. Addresses need no lookup.
func splitAllow(allow []string) (hosts, v4, v6 []string, err error) {
	for _, a := range allow {
		addr, aerr := netip.ParseAddr(a)
		prefix, perr := netip.ParsePrefix(a)
		switch {
		case aerr == nil && addr.Is4(), perr == nil && prefix.Addr().Is4():
			v4 = append(v4, a)
		case aerr == nil, perr == nil:
			v6 = append(v6, a)
		case fleet.ValidHost(a):
			hosts = append(hosts, a)
		default:
			return nil, nil, nil, fmt.Errorf("spec.egress: %q is not a host name or address", a)
		}
	}
	return hosts, v4, v6, nil
}

// moved are what belongs to a network namespace, and so to its owner. Compose
// refuses them on a service that joins another's.
var egressMoved = []string{"networks", "ports", "expose", "hostname", "domainname", "dns", "dns_search", "dns_opt", "extra_hosts", "mac_address", "links", "sysctls"}

func isCaddyLabel(k string) bool {
	return k == "caddy" || strings.HasPrefix(k, "caddy.") || strings.HasPrefix(k, "caddy_")
}

// WithEgress puts each service behind a sidecar that owns its network and allows
// only listed destinations out, plus private networks: replies, its own stack,
// the ingress and the LAN keep working.
//
// The sidecar owns the namespace, not the service, so rules survive a restart of
// the service, and the service can't start before the rules are in place: if the
// sidecar fails, the service stays down rather than going out unfiltered.
func WithEgress(compose []byte, allow []string) ([]byte, error) {
	return egressSidecars(compose, allow, false)
}

// WithEgressLog puts the same sidecars in place, but they block nothing: they only
// record names looked up and connections started, in their logs, for
// nepfleet egress suggest.
func WithEgressLog(compose []byte) ([]byte, error) {
	return egressSidecars(compose, nil, true)
}

// egressLogScript records DNS answers (on loopback: Docker's resolver rewrites the
// ports of the questions, not of the answers) and new connections going out.
const egressLogScript = `set -u
tcpdump -l -n -p -v -i lo -Z root -s 512 'udp and src port 53' &
tcpdump -l -n -p -i any -Q out -Z root -s 128 '(tcp[tcpflags] & tcp-syn != 0 and tcp[tcpflags] & tcp-ack == 0) or (udp and dst port 53 and not dst host 127.0.0.11)' &
sleep 1
touch /run/nepfleet-egress-ready
wait
`

func egressSidecars(compose []byte, allow []string, logOnly bool) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return compose, nil
	}
	services := mapGet(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return compose, nil
	}
	hosts, v4, v6, err := splitAllow(allow)
	if err != nil {
		return nil, err
	}
	var added []*yaml.Node
	owners := map[string]string{}
	for i := 0; i+1 < len(services.Content); i += 2 {
		name, svc := services.Content[i].Value, services.Content[i+1]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		if !logOnly {
			if err := egressCaps(name, svc); err != nil {
				return nil, err
			}
		}
		if nm := mapGet(svc, "network_mode"); nm != nil {
			if nm.Value == "none" || strings.HasPrefix(nm.Value, "service:") {
				continue // no network, or another service's, handled below
			}
			return nil, fmt.Errorf("spec.egress: service %s uses network_mode %s, which this can't filter; set mode off for this stack", name, nm.Value)
		}
		if mapGet(services, name+"-egress") != nil {
			return nil, fmt.Errorf("spec.egress: a service is already called %s-egress", name)
		}
		side := &yaml.Node{Kind: yaml.MappingNode}
		mapSet(side, "image", str(egressImage))
		mapSet(side, "restart", str("unless-stopped"))
		mapSet(side, "cap_drop", seq("ALL"))
		if logOnly {
			mapSet(side, "cap_add", seq("NET_RAW", "SETUID", "SETGID")) // tcpdump changes its user
		} else {
			mapSet(side, "cap_add", seq("NET_ADMIN", "NET_RAW", "SETUID", "SETGID")) // tcpdump changes its user
		}
		mapSet(side, "read_only", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"})
		mapSet(side, "tmpfs", seq("/run"))
		mapSet(side, "security_opt", seq("no-new-privileges:true"))
		if logOnly {
			mapSet(side, "command", seq("/bin/sh", "-c", egressLogScript))
		} else {
			env := &yaml.Node{Kind: yaml.MappingNode}
			mapSet(env, "NEPFLEET_EGRESS_HOSTS", str(strings.Join(hosts, " ")))
			mapSet(env, "NEPFLEET_EGRESS_V4", str(strings.Join(v4, " ")))
			mapSet(env, "NEPFLEET_EGRESS_V6", str(strings.Join(v6, " ")))
			mapSet(side, "environment", env)
			mapSet(side, "command", seq("/bin/sh", "-c", egressScript))
		}
		health := &yaml.Node{Kind: yaml.MappingNode}
		mapSet(health, "test", seq("CMD", "test", "-f", "/run/nepfleet-egress-ready"))
		mapSet(health, "interval", str("2s"))
		mapSet(health, "timeout", str("2s"))
		mapSet(health, "retries", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "30"})
		mapSet(side, "healthcheck", health)

		for _, key := range egressMoved {
			if v := mapGet(svc, key); v != nil {
				mapSet(side, key, v)
				mapDelete(svc, key)
			}
		}
		// Others still reach it by its service name.
		mapSet(side, "networks", withAlias(mapGet(side, "networks"), name))
		// Caddy finds its upstream by the labelled container's address.
		if labels := mapGet(svc, "labels"); labels != nil {
			if moved := takeCaddyLabels(labels); moved != nil {
				mapSet(side, "labels", moved)
			}
		}
		sideName := name + "-egress"
		owners[name] = sideName
		if !logOnly {
			dropNetRaw(svc)
		}
		mapSet(svc, "network_mode", str("service:"+sideName))
		mapSet(svc, "depends_on", dependOnHealthy(mapGet(svc, "depends_on"), sideName))
		added = append(added, &yaml.Node{Kind: yaml.ScalarNode, Value: sideName}, side)
	}
	// Joining a service now means joining its sidecar: namespaces don't chain.
	for i := 0; i+1 < len(services.Content); i += 2 {
		svc := services.Content[i+1]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		nm := mapGet(svc, "network_mode")
		if nm == nil {
			continue
		}
		if target, ok := strings.CutPrefix(nm.Value, "service:"); ok && owners[target] != "" {
			nm.Value = "service:" + owners[target]
			if !logOnly {
				dropNetRaw(svc)
			}
		}
	}
	services.Content = append(services.Content, added...)
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func str(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: s}
}

func seq(items ...string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.SequenceNode}
	for _, s := range items {
		n.Content = append(n.Content, str(s))
	}
	return n
}

// withAlias gives every network the service's name, as a mapping.
func withAlias(networks *yaml.Node, alias string) *yaml.Node {
	out := &yaml.Node{Kind: yaml.MappingNode}
	addAlias := func(name string, cfg *yaml.Node) {
		if cfg == nil || cfg.Kind != yaml.MappingNode {
			cfg = &yaml.Node{Kind: yaml.MappingNode}
		}
		aliases := mapGet(cfg, "aliases")
		if aliases == nil || aliases.Kind != yaml.SequenceNode {
			aliases = &yaml.Node{Kind: yaml.SequenceNode}
			mapSet(cfg, "aliases", aliases)
		}
		aliases.Content = append(aliases.Content, str(alias))
		mapSet(out, name, cfg)
	}
	switch {
	case networks == nil:
		addAlias("default", nil)
	case networks.Kind == yaml.SequenceNode:
		for _, n := range networks.Content {
			addAlias(n.Value, nil)
		}
	case networks.Kind == yaml.MappingNode:
		for i := 0; i+1 < len(networks.Content); i += 2 {
			addAlias(networks.Content[i].Value, networks.Content[i+1])
		}
	}
	return out
}

// takeCaddyLabels moves Caddy's labels out, keeping the form they were in.
func takeCaddyLabels(labels *yaml.Node) *yaml.Node {
	moved := &yaml.Node{Kind: labels.Kind}
	var kept []*yaml.Node
	switch labels.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(labels.Content); i += 2 {
			if isCaddyLabel(labels.Content[i].Value) {
				moved.Content = append(moved.Content, labels.Content[i], labels.Content[i+1])
			} else {
				kept = append(kept, labels.Content[i], labels.Content[i+1])
			}
		}
	case yaml.SequenceNode:
		for _, n := range labels.Content {
			k, _, _ := strings.Cut(n.Value, "=")
			if isCaddyLabel(k) {
				moved.Content = append(moved.Content, n)
			} else {
				kept = append(kept, n)
			}
		}
	default:
		return nil
	}
	labels.Content = kept
	if len(moved.Content) == 0 {
		return nil
	}
	return moved
}

// dependOnHealthy adds the sidecar, in map form, keeping what was there.
func dependOnHealthy(deps *yaml.Node, side string) *yaml.Node {
	out := &yaml.Node{Kind: yaml.MappingNode}
	if deps != nil {
		switch deps.Kind {
		case yaml.MappingNode:
			out.Content = deps.Content
		case yaml.SequenceNode:
			for _, n := range deps.Content {
				cond := &yaml.Node{Kind: yaml.MappingNode}
				mapSet(cond, "condition", str("service_started"))
				mapSet(out, n.Value, cond)
			}
		}
	}
	cond := &yaml.Node{Kind: yaml.MappingNode}
	mapSet(cond, "condition", str("service_healthy"))
	mapSet(out, side, cond)
	return out
}

// egressCaps refuses what could simply undo the rules: they live in a namespace
// the service shares.
func egressCaps(name string, svc *yaml.Node) error {
	if p := mapGet(svc, "privileged"); p != nil && p.Value == "true" {
		return fmt.Errorf("spec.egress: service %s is privileged and could remove the rules; set mode off for this stack", name)
	}
	if caps := mapGet(svc, "cap_add"); caps != nil {
		for _, c := range caps.Content {
			switch strings.TrimPrefix(strings.ToUpper(c.Value), "CAP_") {
			case "NET_ADMIN", "ALL":
				return fmt.Errorf("spec.egress: service %s has %s and could remove the rules; set mode off for this stack", name, c.Value)
			}
		}
	}
	return nil
}

// dropNetRaw takes away Docker's default raw sockets: packet sockets pass by
// iptables. A service that asks for the capability keeps it.
func dropNetRaw(svc *yaml.Node) {
	isRaw := func(n *yaml.Node) bool {
		v := strings.TrimPrefix(strings.ToUpper(n.Value), "CAP_")
		return v == "NET_RAW" || v == "ALL"
	}
	if caps := mapGet(svc, "cap_add"); caps != nil && slices.ContainsFunc(caps.Content, isRaw) {
		return
	}
	drop := mapGet(svc, "cap_drop")
	if drop == nil || drop.Kind != yaml.SequenceNode {
		mapSet(svc, "cap_drop", seq("NET_RAW"))
		return
	}
	if !slices.ContainsFunc(drop.Content, isRaw) {
		drop.Content = append(drop.Content, str("NET_RAW"))
	}
}

// enforceEgress applies mode enforce, for compose stacks: swarm has no shared
// network namespaces.
func enforceEgress(compose []byte, in Input, env map[string]string) ([]byte, error) {
	mode, allow := fleet.EffectiveEgress(in.Fleet, in.Stack)
	if mode != fleet.EgressEnforce && mode != fleet.EgressLog {
		return compose, nil
	}
	if in.Stack.Spec.Mode == "swarm" {
		return nil, fmt.Errorf("spec.egress: mode %s is for compose stacks; swarm has no shared network namespaces", mode)
	}
	if mode == fleet.EgressLog {
		return WithEgressLog(compose)
	}
	return WithEgress(compose, EgressAllow(allow, in.Template, env))
}
