// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Serraniel/nepfleet/internal/doctor"
)

// lookupHost resolves a name to its addresses (net.DefaultResolver.LookupHost).
type lookupHost func(ctx context.Context, host string) ([]string, error)

// domainChecks says whether each stack's domain resolves: Caddy gets a
// certificate over DNS-01 without the site's A/AAAA record, and the site is
// still unreachable. domains maps a stack to its DOMAIN.
func domainChecks(ctx context.Context, lookup lookupHost, domains map[string]string) []doctor.Check {
	type result struct {
		addrs []string
		err   error
	}
	results := make(map[string]result, len(domains))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for stack, domain := range domains {
		if !checkableDomain(domain) {
			continue
		}
		wg.Go(func() {
			lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			addrs, err := lookup(lctx, domain)
			mu.Lock()
			results[stack] = result{addrs, err}
			mu.Unlock()
		})
	}
	wg.Wait()
	var checks []doctor.Check
	for _, stack := range slices.Sorted(maps.Keys(results)) {
		domain, r := domains[stack], results[stack]
		var dnsErr *net.DNSError
		switch {
		case r.err == nil && len(r.addrs) > 0:
			checks = append(checks, doctor.Check{Name: "DNS", Status: doctor.OK,
				Detail: fmt.Sprintf("%s: %s resolves to %s", stack, domain, strings.Join(r.addrs, ", "))})
		case r.err == nil || errors.As(r.err, &dnsErr) && dnsErr.IsNotFound:
			checks = append(checks, doctor.Check{Name: "DNS", Status: doctor.Fail,
				Detail: fmt.Sprintf("%s: %s doesn't resolve, so the site can't be reached (its certificate doesn't need the record)", stack, domain),
				Fix:    fmt.Sprintf("create an A (and AAAA) record for %s pointing at the host, e.g. one wildcard record for all its sites; or let Caddy keep the records itself (its option \"Create the sites' DNS records\"); for a LAN-only name, add it to the local DNS server", domain)})
		default:
			checks = append(checks, doctor.Check{Name: "DNS", Status: doctor.Warn,
				Detail: fmt.Sprintf("%s: couldn't look up %s: %v", stack, domain, r.err),
				Fix:    "check this host's DNS resolver, then run doctor again"})
		}
	}
	return checks
}

// checkableDomain says whether a DOMAIN value is a name to look up: not an
// address (a stack on a LAN port gets the host's) and not a single label.
func checkableDomain(d string) bool {
	if d == "" || net.ParseIP(d) != nil || !strings.Contains(d, ".") || strings.ContainsAny(d, " */:") {
		return false
	}
	return !strings.EqualFold(d, "localhost") && !strings.HasSuffix(strings.ToLower(d), ".localhost")
}
