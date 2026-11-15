// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/Serraniel/nepfleet/internal/doctor"
)

func TestDomainChecks(t *testing.T) {
	lookup := func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "monitor.vps.example.org":
			return []string{"192.0.2.10"}, nil
		case "status.vps.example.org":
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		case "slow.example.org":
			return nil, &net.DNSError{Err: "i/o timeout", Name: host, IsTimeout: true}
		}
		return nil, errors.New("unexpected lookup of " + host)
	}
	checks := domainChecks(context.Background(), lookup, map[string]string{
		"dozzle": "monitor.vps.example.org",
		"kuma":   "status.vps.example.org",
		"slow":   "slow.example.org",
		"lan":    "192.168.1.5", // a stack on a LAN port: the host's address
		"single": "nas",
		"wild":   "*.vps.example.org",
	})
	want := []struct {
		status doctor.Status
		prefix string
	}{
		{doctor.OK, "dozzle: monitor.vps.example.org resolves to 192.0.2.10"},
		{doctor.Fail, "kuma: status.vps.example.org doesn't resolve"},
		{doctor.Warn, "slow: couldn't look up slow.example.org"},
	}
	if len(checks) != len(want) {
		t.Fatalf("checks = %+v", checks)
	}
	for i, w := range want {
		c := checks[i]
		if c.Name != "DNS" || c.Status != w.status || len(c.Detail) < len(w.prefix) || c.Detail[:len(w.prefix)] != w.prefix {
			t.Errorf("check %d = %+v, want %v %q", i, c, w.status, w.prefix)
		}
	}
	if checks[1].Fix == "" {
		t.Error("a failing check needs a fix")
	}
}
