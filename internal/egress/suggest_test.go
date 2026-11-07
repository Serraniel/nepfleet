// SPDX-License-Identifier: AGPL-3.0-or-later

package egress

import (
	"regexp"
	"slices"
	"testing"
)

// Lines as tcpdump writes them in the sidecar; TestEgressLogInDocker checks real ones.
const sampleLogs = `dropped privs to root
listening on lo, link-type EN10MB (Ethernet), snapshot length 512 bytes
18:08:39.236271 IP (tos 0x0, ttl 64, id 1, offset 0, flags [DF], proto UDP (17), length 93)
    127.0.0.11.53 > 127.0.0.1.51595: 3178 2/0/0 one.one.one.one. A 1.0.0.1, one.one.one.one. A 1.1.1.1 (65)
    127.0.0.11.53 > 127.0.0.1.51595: 5009 2/0/0 one.one.one.one. AAAA 2606:4700:4700::1001, one.one.one.one. AAAA 2606:4700:4700::1111 (89)
    127.0.0.11.53 > 127.0.0.1.1: 7 1/0/0 unused.example.org. A 8.8.4.4 (50)
    127.0.0.11.53 > 127.0.0.1.1: 7 1/0/0 db. A 172.18.0.5 (50)
    127.0.0.11.53 > 127.0.0.1.1: 7 1/0/0 printer.local. A 192.168.1.9 (50)
    127.0.0.11.53 > 127.0.0.1.1: 7 1/0/0 internal.example.org. A 10.0.0.5 (50)
    127.0.0.11.53 > 127.0.0.1.1: 9 NXDomain q: A? missing.example.org. 0/1/0 (40)
    127.0.0.11.53 > 127.0.0.1.1: 8 2/0/0 cdn.example.org. CNAME edge.provider.test., edge.provider.test. A 4.4.4.4 (70)
18:08:39.293149 eth0  Out IP 192.168.80.2.54552 > 1.0.0.1.443: Flags [S], seq 1, win 64240, length 0
18:08:39.293149 eth0  Out IP 192.168.80.2.54553 > 9.9.9.9.443: Flags [S], seq 2, win 64240, length 0
18:08:39.293149 eth0  Out IP 192.168.80.2.54555 > 4.4.4.4.443: Flags [S], seq 2, win 64240, length 0
18:08:39.293149 eth0  Out IP 192.168.80.2.54554 > 10.0.0.5.5432: Flags [S], seq 3, win 64240, length 0
18:08:39.293149 eth0  Out IP6 fd00::1.54554 > 2606:4700:4700::1111.443: Flags [S], seq 3, win 64240, length 0
18:08:39.293149 eth0  Out IP 192.168.80.2.40000 > 8.8.8.8.53: 1234+ A? direct.example.org. (36)
garbage line with > 300.1.1.1.443: Flags [S]
`

func TestParse(t *testing.T) {
	r := Parse(sampleLogs)
	// The name asked for, not where a CNAME led: that's what the app will ask again.
	if !slices.Equal(r.Hosts, []string{"cdn.example.org", "one.one.one.one"}) {
		t.Errorf("hosts: %v", r.Hosts)
	}
	if !slices.Equal(r.Addresses, []string{"8.8.8.8", "9.9.9.9"}) {
		t.Errorf("addresses: %v", r.Addresses)
	}
	if !slices.Equal(r.LookedUp, []string{"internal.example.org", "missing.example.org", "unused.example.org"}) {
		t.Errorf("looked up: %v", r.LookedUp)
	}
}

func TestWithout(t *testing.T) {
	r := Report{Hosts: []string{"a.test", "b.test"}, LookedUp: []string{"c.test"}, Addresses: []string{"9.9.9.9", "1.1.1.1", "8.8.8.8"}}
	got := r.Without([]string{"a.test", "c.test", "9.9.9.9", "1.1.0.0/16"})
	if !slices.Equal(got.Hosts, []string{"b.test"}) || len(got.LookedUp) != 0 || !slices.Equal(got.Addresses, []string{"8.8.8.8"}) {
		t.Errorf("%+v", got)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(sampleLogs)
	f.Fuzz(func(t *testing.T, logs string) {
		r := Parse(logs)
		safe := regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
		for _, h := range append(r.Hosts, r.LookedUp...) {
			if !safe.MatchString(h) {
				t.Fatalf("a name from logs is %q: it goes into a YAML list", h)
			}
		}
	})
}
