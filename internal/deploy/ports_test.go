// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"net"
	"testing"

	"github.com/Serraniel/nepfleet/internal/render"
)

func TestPublishedPorts(t *testing.T) {
	out := &render.Output{Files: map[string][]byte{
		render.EnvFile: []byte("WEB='8096'\n"),
		render.ComposeFile: []byte(`services:
  a:
    ports:
      - "8080:80"
      - "127.0.0.1:9000:9000"
      - "[::1]:9001:9001"
      - "0.0.0.0:53:53/udp"
      - "7000-7002:7000-7002"
      - "${WEB}:8096"
      - "3000"
      - 3001
      - nonsense
  b:
    ports:
      - target: 80
        published: 8443
        protocol: tcp
      - target: 81
        published: "8500-8501"
        host_ip: 10.0.0.1
      - target: 82
`),
	}}
	got := map[Published]bool{}
	for _, p := range PublishedPorts(out) {
		got[p] = true
	}
	for _, want := range []Published{
		{"", 8080, "tcp"}, {"127.0.0.1", 9000, "tcp"}, {"::1", 9001, "tcp"}, {"", 53, "udp"},
		{"", 7000, "tcp"}, {"", 7001, "tcp"}, {"", 7002, "tcp"}, {"", 8096, "tcp"},
		{"", 8443, "tcp"}, {"10.0.0.1", 8500, "tcp"}, {"10.0.0.1", 8501, "tcp"},
	} {
		if !got[want] {
			t.Errorf("missing %+v in %v", want, got)
		}
	}
	if len(got) != 11 {
		t.Errorf("container-only and odd entries must not count: %v", got)
	}
}

func TestPortRangeIsBounded(t *testing.T) {
	if got := parsePort("1-65535:1-65535"); len(got) != 1 {
		t.Errorf("%d ports", len(got))
	}
}

func TestPortClashes(t *testing.T) {
	web := Published{"", 443, "tcp"}
	got := PortClashes(map[string][]Published{
		"a": {web, {"127.0.0.1", 9000, "tcp"}, {"", 53, "udp"}},
		"b": {web, web},
		"c": {{"127.0.0.2", 9000, "tcp"}, {"", 53, "tcp"}},
		"d": {{"", 9000, "tcp"}},
	})
	want := map[PortClash]bool{{443, "a", "b"}: true, {9000, "a", "d"}: true, {9000, "c", "d"}: true}
	if len(got) != len(want) {
		t.Errorf("%+v", got)
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("unexpected %+v", c)
		}
	}
}

func TestPortBusy(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // it is a TCP listener
	if !PortBusy(Published{"127.0.0.1", port, "tcp"}) {
		t.Error("a held port reads as free")
	}
	_ = l.Close()
	if PortBusy(Published{"127.0.0.1", port, "tcp"}) {
		t.Error("a free port reads as held")
	}
	if PortBusy(Published{"127.0.0.1", port, "udp"}) {
		t.Error("udp isn't tested")
	}
}
