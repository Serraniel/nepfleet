// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/Serraniel/nepfleet/internal/doctor"
)

// DoctorCheck is one of nepfleet doctor's checks, as `doctor --json` prints
// it and the app shows it.
type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
	// Action names a setup kind that applies the fix as a reviewed plan
	// (start, repair), when the app can do it.
	Action string `json:"action,omitempty"`
}

// doctorScript runs nepfleet doctor where NepFleet is: in its container, else
// the host's binary. Older versions have no --json: their text is sent.
const doctorScript = `set -u
sel='--filter label=com.docker.compose.project=nepfleet --filter label=com.docker.compose.service=nepfleet'
c=$(docker ps -q $sel 2>/dev/null | head -n1)
if [ -n "$c" ]; then
  out=$(docker exec "$c" nepfleet doctor --json 2>/dev/null)
  case "$out" in "["*) printf '%s\n' "$out" ;; *) docker exec "$c" nepfleet doctor 2>&1 ;; esac
elif docker ps -aq $sel 2>/dev/null | grep -q .; then
  echo 'nepfleet-doctor: stopped'
elif command -v nepfleet >/dev/null 2>&1; then
  out=$(nepfleet doctor --json 2>/dev/null)
  case "$out" in "["*) printf '%s\n' "$out" ;; *) nepfleet doctor 2>&1 ;; esac
else
  echo 'nepfleet-doctor: absent'
fi
true
`

// Doctor runs nepfleet doctor on the machine over SSH and returns its checks.
// A NepFleet that isn't running, or isn't there, is a failed check itself.
func Doctor(ctx context.Context, run func(context.Context, Target, string) ([]byte, error), t Target) ([]DoctorCheck, error) {
	out, err := run(ctx, t, doctorScript)
	if err != nil {
		return nil, err
	}
	return parseDoctor(out), nil
}

// textCheckRe and textFixRe read doctor's text output (" ✓ Name   detail",
// then "   → fix"), from NepFleet versions without --json.
var (
	textCheckRe = regexp.MustCompile(`^ ([✓⚠✗]) (.{20}) ?(.*)$`)
	textFixRe   = regexp.MustCompile(`^\s+→ (.*)$`)
)

func parseDoctor(out []byte) []DoctorCheck {
	text := strings.TrimSpace(string(out))
	switch {
	case strings.Contains(text, "nepfleet-doctor: stopped"):
		return []DoctorCheck{{Name: "NepFleet", Status: "fail", Detail: "its container is stopped",
			Fix: "start it on the machine: cd /srv/nepfleet && docker compose up -d", Action: "start"}}
	case strings.Contains(text, "nepfleet-doctor: absent"):
		return []DoctorCheck{{Name: "NepFleet", Status: "fail", Detail: "not installed on this machine",
			Fix: "set it up from Fleets (Set up NepFleet here)"}}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "[") {
			var checks []DoctorCheck
			if json.Unmarshal([]byte(line), &checks) == nil {
				for i := range checks {
					checks[i] = checks[i].clean()
				}
				return checks
			}
		}
	}
	var checks []DoctorCheck
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if m := textCheckRe.FindStringSubmatch(line); m != nil {
			status := map[string]string{"✓": "ok", "⚠": "warn", "✗": "fail"}[m[1]]
			checks = append(checks, DoctorCheck{Name: strings.TrimSpace(m[2]), Status: status, Detail: m[3]}.clean())
		} else if m := textFixRe.FindStringSubmatch(line); m != nil && len(checks) > 0 {
			checks[len(checks)-1].Fix = Clean(m[1])
		}
	}
	if len(checks) == 0 {
		// Something else answered: say what, briefly.
		detail := Clean(text)
		if len(detail) > 300 {
			detail = detail[:300] + "…"
		}
		return []DoctorCheck{{Name: "NepFleet", Status: "fail", Detail: "doctor didn't run: " + detail}}
	}
	return checks
}

// clean keeps another machine's text printable, like the probe's report.
// The action is this side's: never one the machine names.
func (c DoctorCheck) clean() DoctorCheck {
	c.Name, c.Detail, c.Fix = Clean(c.Name), Clean(c.Detail), Clean(c.Fix)
	switch c.Status {
	case "ok", "warn", "fail":
	default:
		c.Status = "warn"
	}
	// The machine may name one action: dns-records, a call to its own API
	// (the app asks it), never a plan this side runs over SSH.
	fromMachine := c.Action
	c.Action = ""
	if fromMachine == "dns-records" && c.Name == "DNS" && c.Status == "fail" {
		c.Action = fromMachine
	}
	if c.Status != "ok" && (c.Name == "age key" && c.Detail == doctor.NoRecipients ||
		c.Name == "Fleet repository" && c.Detail == doctor.NoCommits) {
		c.Action = "repair"
	}
	return c
}
