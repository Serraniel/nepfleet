// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Serraniel/nepfleet/internal/api"
	"github.com/Serraniel/nepfleet/internal/doctor"
	"github.com/Serraniel/nepfleet/internal/fleet"
)

// DNSRecordsAction is the doctor's action on a DNS check that Caddy's
// "Create the sites' DNS records" option fixes (api.SetDNSRecords).
const DNSRecordsAction = "dns-records"

// KeyBackupAction is the doctor's action on a Key copy warning that a backup
// through POST /api/v1/key/backup clears: that records the copy.
const KeyBackupAction = "key-backup"

// zoneRe is a DNS zone as the Caddy template's DNS_ZONE takes it.
var zoneRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// caddyDNSRecords finds the Caddy stack on this host that could keep its
// sites' DNS records: from a template that has the option, with a DNS
// provider set. It returns the stack, or why there is none to turn on.
func caddyDNSRecords(e *env) (*fleet.Stack, error) {
	all, err := e.fleet.Stacks()
	if err != nil {
		return nil, err
	}
	for _, s := range all {
		ref := s.Spec.Template
		if ref == nil || ref.Name != "caddy" || (s.Spec.Target != e.host && s.Spec.Target != "local") {
			continue
		}
		tpl, err := e.pinnedTemplate(s)
		if err != nil {
			return nil, err
		}
		inputs := map[string]string{}
		for _, in := range tpl.Spec.Inputs {
			inputs[in.Name] = in.Default
		}
		if _, ok := inputs["DNS_RECORDS"]; !ok {
			return nil, api.InvalidError{Reason: "the Caddy template of " + s.Metadata.Name + " (" + tpl.Spec.Version +
				") has no DNS records option yet: it comes with a newer template (templates 0.4.0, or the template source on ref main)"}
		}
		value := func(k string) string { return cmp.Or(s.Spec.Values[k].Literal, inputs[k]) }
		if p := value("CADDY_DNS_PROVIDER"); p == "" || p == "none" {
			return nil, api.InvalidError{Reason: s.Metadata.Name + " has no DNS provider: set one (and its token) first"}
		}
		if value("DNS_RECORDS") == "on" && !strings.HasSuffix(value("DNS_ZONE"), "example.org") {
			return nil, api.InvalidError{Reason: s.Metadata.Name + " keeps the DNS records already (zone " + value("DNS_ZONE") +
				"): it checks every 5 minutes; if they still don't appear, check that its token may edit DNS in that zone, and Caddy's log"}
		}
		return s, nil
	}
	return nil, api.InvalidError{Reason: "no Caddy stack runs on this host"}
}

// setValues sets literal values in a stack.yaml's spec.values, keeping its
// comments.
func setValues(raw []byte, values map[string]string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, errors.New("stack.yaml is empty")
	}
	spec, err := mappingChild(doc.Content[0], "spec", yaml.MappingNode)
	if err != nil {
		return nil, err
	}
	vals, err := mappingChild(spec, "values", yaml.MappingNode)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		// Quoted: "on" would be a boolean to some YAML readers.
		v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: values[k], Style: yaml.DoubleQuotedStyle}
		set := false
		for i := 0; i+1 < len(vals.Content); i += 2 {
			if vals.Content[i].Value == k {
				vals.Content[i+1] = v
				set = true
			}
		}
		if !set {
			vals.Content = append(vals.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, v)
		}
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return b.Bytes(), enc.Close()
}

// dnsRecordsAPI is POST /api/v1/dns-records: it turns on Caddy's "Create the
// sites' DNS records" option for zone, in the Caddy stack's stack.yaml, and
// commits that as by. It doesn't apply: the stack's plan shows the change.
func dnsRecordsAPI(c common, zone, by string) (api.DNSRecordsResult, error) {
	zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	if !zoneRe.MatchString(zone) {
		return api.DNSRecordsResult{}, api.InvalidError{Reason: "the zone is a domain such as example.org"}
	}
	if !apiMu.TryLock() {
		return api.DNSRecordsResult{}, api.ErrBusy
	}
	defer apiMu.Unlock()
	e, err := c.open()
	if err != nil {
		return api.DNSRecordsResult{}, err
	}
	s, err := caddyDNSRecords(e)
	if err != nil {
		return api.DNSRecordsResult{}, err
	}
	path := filepath.Join(s.Dir, "stack.yaml")
	rel := relTo(e.fleet.Dir, path)
	if st, err := gitOut(e.fleet.Dir, "status", "--porcelain", "--", rel); err != nil {
		return api.DNSRecordsResult{}, err
	} else if strings.TrimSpace(st) != "" {
		return api.DNSRecordsResult{}, api.ErrUncommitted
	}
	raw, err := os.ReadFile(path) //nolint:gosec // a stack's own file
	if err != nil {
		return api.DNSRecordsResult{}, err
	}
	out, err := setValues(raw, map[string]string{"DNS_RECORDS": "on", "DNS_ZONE": zone})
	if err != nil {
		return api.DNSRecordsResult{}, api.InvalidError{Reason: "stack.yaml: " + err.Error()}
	}
	if err := os.WriteFile(path, out, 0o600); err != nil { //nolint:gosec // a stack's own file
		return api.DNSRecordsResult{}, err
	}
	restore := func() { _ = os.WriteFile(path, raw, 0o600) } //nolint:gosec // as it was
	if s2, err := e.fleet.LoadStack(s.Metadata.Name); err != nil {
		restore()
		return api.DNSRecordsResult{}, api.InvalidError{Reason: cleanReason(err, e.fleet.Dir)}
	} else if _, err := e.prepare(s2); err != nil {
		restore()
		return api.DNSRecordsResult{}, api.InvalidError{Reason: cleanReason(err, s.Dir, e.fleet.Dir)}
	}
	msg := "chore(" + s.Metadata.Name + "): let Caddy keep the sites' DNS records in " + zone
	if err := commitAs(e.fleet.Dir, apiAuthor(by), msg, rel); err != nil {
		restore()
		return api.DNSRecordsResult{}, err
	}
	head, err := gitOut(e.fleet.Dir, "rev-parse", "HEAD")
	return api.DNSRecordsResult{Stack: s.Metadata.Name, Commit: strings.TrimSpace(head)}, err
}

// dnsRecordsFixable says whether the doctor can offer DNSRecordsAction here.
func dnsRecordsFixable(e *env) bool {
	_, err := caddyDNSRecords(e)
	return err == nil
}

// doctorAction is the fix the app can start for a check, or "": for a domain
// that doesn't resolve, Caddy's DNS records, when this host's Caddy can keep
// them; for a key without a known copy, a backup. Worked out once per doctor
// run.
func doctorAction(c common) func(doctor.Check) string {
	fixable := false
	if e, err := c.open(); err == nil {
		fixable = dnsRecordsFixable(e)
	}
	return func(ch doctor.Check) string {
		if fixable && ch.Name == "DNS" && ch.Status == doctor.Fail {
			return DNSRecordsAction
		}
		// Only when no copy is known: with the key from the environment, a
		// backup of the key file wouldn't clear the warning.
		if ch.Name == "Key copy" && ch.Status == doctor.Warn && ch.Detail == noKeyCopy {
			return KeyBackupAction
		}
		return ""
	}
}
