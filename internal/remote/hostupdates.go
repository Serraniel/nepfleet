// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"bufio"
	"bytes"
	"context"
	"regexp"
	"strconv"
	"strings"
)

// HostUpdates is what a machine's package manager has pending, read as the
// SSH user without installing anything (docs/host-updates.md).
type HostUpdates struct {
	OS        string `json:"os"`        // the distribution's ID, from os-release
	Supported bool   `json:"supported"` // whether NepFleet knows its package manager
	Pending   int    `json:"pending"`   // packages with an update
	// Security is how many of them fix a vulnerability; -1 when unknown
	// (Arch without arch-audit).
	Security int `json:"security"`
	// RebootRequired is nil when the machine can't tell.
	RebootRequired *bool    `json:"rebootRequired"`
	Packages       []string `json:"packages,omitempty"` // the first maxPackages names
	Notes          []string `json:"notes,omitempty"`    // how fresh or complete this is
}

const maxPackages = 200

// hostUpdatesScript prints key=value lines; package names as pkg=<name>.
// Nothing needs root: checkupdates syncs a database of its own, apt and dnf
// read their lists as last refreshed.
const hostUpdatesScript = `set -u
. /etc/os-release 2>/dev/null || true
echo "os=${ID:-unknown}"
case "${ID:-}:${ID_LIKE:-}" in
arch:*|manjaro:*|endeavouros:*|*:*arch*)
  if command -v checkupdates >/dev/null 2>&1; then
    list=$(checkupdates 2>/dev/null)
  else
    list=$(pacman -Qu 2>/dev/null)
    echo "note=pacman-contrib isn't installed, so this is from the package database as of the last pacman -Sy"
  fi
  printf '%s\n' "$list" | awk 'NF { print "pkg=" $1 }'
  if command -v arch-audit >/dev/null 2>&1; then
    echo "security=$(arch-audit --upgradable --quiet 2>/dev/null | grep -c .)"
  else
    echo "note=arch-audit isn't installed, so security fixes aren't counted"
  fi
  if [ -d "/usr/lib/modules/$(uname -r)" ]; then echo reboot=no; else echo reboot=yes; fi
  ;;
debian:*|ubuntu:*|raspbian:*|*:*debian*|*:*ubuntu*)
  list=$(apt list --upgradable 2>/dev/null | tail -n +2)
  printf '%s\n' "$list" | awk -F/ 'NF > 1 { print "pkg=" $1 }'
  echo "security=$(printf '%s\n' "$list" | grep -c -- '-security')"
  if [ -f /var/run/reboot-required ]; then echo reboot=yes; else echo reboot=no; fi
  echo "note=from the package lists as of the last apt update"
  ;;
fedora:*|rhel:*|centos:*|rocky:*|almalinux:*|*:*fedora*|*:*rhel*)
  dnf -q check-update 2>/dev/null | awk 'NF == 3 && $1 ~ /\./ { sub(/\.[^.]*$/, "", $1); print "pkg=" $1 }'
  echo "security=$(dnf -q updateinfo list --security 2>/dev/null | grep -c .)"
  dnf needs-restarting -r >/dev/null 2>&1
  case $? in 0) echo reboot=no ;; 1) echo reboot=yes ;; *) echo reboot=unknown ;; esac
  ;;
*)
  echo unsupported=yes
  ;;
esac
true
`

var (
	pkgNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._:@-]{0,127}$`)
	osIDRe    = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)
)

// CheckHostUpdates asks the machine's package manager over SSH what it has
// pending. It changes nothing on the machine.
func CheckHostUpdates(ctx context.Context, run func(context.Context, Target, string) ([]byte, error), t Target) (*HostUpdates, error) {
	out, err := run(ctx, t, hostUpdatesScript)
	if err != nil {
		return nil, err
	}
	return parseHostUpdates(out), nil
}

func parseHostUpdates(out []byte) *HostUpdates {
	h := &HostUpdates{OS: "unknown", Supported: true, Security: -1}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch key {
		case "os":
			if osIDRe.MatchString(value) {
				h.OS = value
			}
		case "unsupported":
			h.Supported = false
		case "pkg":
			if !pkgNameRe.MatchString(value) {
				continue
			}
			h.Pending++
			if len(h.Packages) < maxPackages {
				h.Packages = append(h.Packages, value)
			}
		case "security":
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				h.Security = n
			}
		case "reboot":
			switch value {
			case "yes", "no":
				b := value == "yes"
				h.RebootRequired = &b
			}
		case "note":
			if len(h.Notes) < 5 {
				h.Notes = append(h.Notes, Clean(value))
			}
		}
	}
	if !h.Supported {
		h.Security = -1
	}
	return h
}
