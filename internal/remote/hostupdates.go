// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
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
	// LastUpgrade is when the last full upgrade started (Arch: pacman.log).
	LastUpgrade time.Time `json:"lastUpgrade,omitzero"`
	// Fresh: the package manager asked its mirrors just now (checkupdates,
	// dnf). Otherwise the answer is only as new as ListsFrom, when the
	// package lists were last refreshed: "no updates" then means none known.
	Fresh     bool      `json:"fresh"`
	ListsFrom time.Time `json:"listsFrom,omitzero"`
	// News are Arch news items published since LastUpgrade: they usually
	// announce manual steps, so read them before updating.
	News []NewsItem `json:"news,omitempty"`

	archLike bool
}

// NewsItem is one entry of a distribution's news feed.
type NewsItem struct {
	Title     string    `json:"title"`
	Link      string    `json:"link"`
	Published time.Time `json:"published"`
}

// Arch reports whether the machine runs Arch Linux or a derivative, whose
// news feed applies.
func (h *HostUpdates) Arch() bool { return h.archLike }

const maxPackages = 200

// hostUpdatesScript prints key=value lines; package names as pkg=<name>.
// Nothing needs root: checkupdates syncs a database of its own, apt and dnf
// read their lists as last refreshed.
const hostUpdatesScript = `set -u
# A missing file would end a POSIX shell at ".", even with || true.
if [ -r /etc/os-release ]; then . /etc/os-release; fi
echo "os=${ID:-unknown}"
case "${ID:-}:${ID_LIKE:-}" in
arch:*|manjaro:*|endeavouros:*|*:*arch*)
  echo family=arch
  if command -v checkupdates >/dev/null 2>&1; then
    list=$(checkupdates 2>/dev/null)
    echo fresh=yes
  else
    list=$(pacman -Qu 2>/dev/null)
    echo fresh=no
    echo "listsfrom=$(stat -c %Y /var/lib/pacman/sync/core.db 2>/dev/null)"
    echo "note=install pacman-contrib for a fresh answer: without it, this comes from the package lists of the last pacman -Sy"
  fi
  printf '%s\n' "$list" | awk 'NF { print "pkg=" $1 }'
  if command -v arch-audit >/dev/null 2>&1; then
    echo "security=$(arch-audit --upgradable --quiet 2>/dev/null | grep -c .)"
  else
    echo "note=arch-audit isn't installed, so security fixes aren't counted"
  fi
  if [ -d "/usr/lib/modules/$(uname -r)" ]; then echo reboot=no; else echo reboot=yes; fi
  # When the last full upgrade started, for Arch news published since.
  echo "lastupgrade=$(grep -a 'starting full system upgrade' /var/log/pacman.log 2>/dev/null | tail -n 1 | sed -n 's/^\[\([^]]*\)\].*/\1/p')"
  ;;
debian:*|ubuntu:*|raspbian:*|*:*debian*|*:*ubuntu*)
  list=$(apt list --upgradable 2>/dev/null | tail -n +2)
  printf '%s\n' "$list" | awk -F/ 'NF > 1 { print "pkg=" $1 }'
  echo "security=$(printf '%s\n' "$list" | grep -c -- '-security')"
  if [ -f /var/run/reboot-required ]; then echo reboot=yes; else echo reboot=no; fi
  echo fresh=no
  lists=$(ls -t /var/lib/apt/lists/*Packages* 2>/dev/null | head -n 1)
  if [ -n "$lists" ]; then echo "listsfrom=$(stat -c %Y "$lists" 2>/dev/null)"; fi
  echo "note=this comes from the package lists of the last apt update"
  ;;
fedora:*|rhel:*|centos:*|rocky:*|almalinux:*|*:*fedora*|*:*rhel*)
  # dnf refreshes metadata that has expired before it answers.
  echo fresh=yes
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
		case "family":
			h.archLike = value == "arch"
		case "fresh":
			h.Fresh = value == "yes"
		case "listsfrom":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n > 0 {
				h.ListsFrom = time.Unix(n, 0).UTC()
			}
		case "lastupgrade":
			if ts, err := time.Parse("2006-01-02T15:04:05-0700", value); err == nil {
				h.LastUpgrade = ts.UTC()
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

// HostUpdatePlan installs the machine's pending updates with its package
// manager, as root. It doesn't reboot: when one is required, the check
// says so. On Arch the keyring comes first, then the full upgrade, never a
// partial one; read the Arch news the check lists before running it.
func HostUpdatePlan(r *Report) ([]Step, error) {
	if r.Kernel != "Linux" {
		return nil, fmt.Errorf("NepFleet updates Linux hosts; this is %s", r.Kernel)
	}
	if !r.Root && !r.Sudo && !r.SudoPassword {
		return nil, errors.New("updating the host needs root: log in as root, or as a user who may use sudo")
	}
	const restarts = " (a Docker upgrade restarts Docker, and with it every container)"
	switch r.OSID {
	case "arch", "manjaro", "endeavouros":
		return []Step{diskCheck("/var/cache/pacman/pkg"),
			{Text: "update the package signing keys first (archlinux-keyring)", Root: true,
				Script: "pacman -Sy --needed --noconfirm archlinux-keyring\n"},
			// The checkers come with the full upgrade, never on their own:
			// pacman-contrib's checkupdates gives the check a fresh answer,
			// arch-audit counts security fixes.
			{Text: "install every pending update (pacman -Su), with pacman-contrib and arch-audit if they're missing, for fresh checks" + restarts, Root: true,
				Script: "extra=\nfor p in pacman-contrib arch-audit; do\n" +
					"\tpacman -Qq \"$p\" >/dev/null 2>&1 || { pacman -Siq \"$p\" >/dev/null 2>&1 && extra=\"$extra $p\"; }\n" +
					"done\n" +
					"# shellcheck disable=SC2086 # the package names are fixed words\n" +
					"pacman -Su --noconfirm --needed $extra\n"},
		}, nil
	case "debian", "ubuntu", "raspbian":
		return []Step{diskCheck("/var/cache/apt/archives"),
			{Text: "refresh the package lists (apt-get update)", Root: true,
				Script: "export DEBIAN_FRONTEND=noninteractive\napt-get update -qq\n"},
			{Text: "install every pending update, keeping changed config files (apt-get dist-upgrade)" + restarts, Root: true,
				Script: "export DEBIAN_FRONTEND=noninteractive\napt-get -y -qq -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold dist-upgrade\n"},
		}, nil
	case "fedora":
		return []Step{diskCheck("/var/cache/dnf"),
			{Text: "install every pending update (dnf upgrade)" + restarts, Root: true,
				Script: "dnf -y -q upgrade --refresh\n"},
		}, nil
	}
	return nil, fmt.Errorf("updating %q hosts isn't supported yet: update it with its package manager", r.OSID)
}

// diskCheck is a plan's first step: it stops before anything is installed
// when the package cache's file system, / or a separate /boot (kernel and
// initramfs) hasn't the room an upgrade needs. Half-installed upgrades on a
// full disk are what breaks hosts.
func diskCheck(cache string) Step {
	script := `fail=0
need() {
	[ -d "$1" ] || return 0
	free=$(df -Pk "$1" | awk 'NR==2 { print int($4 / 1024) }')
	if [ "$free" -lt "$2" ]; then
		echo "only ${free} MiB free for $1; the update needs at least $2 MiB there: free some space first" >&2
		fail=1
	fi
}
need / 2048
need ` + cache + ` 2048
if awk '$2 == "/boot" { found = 1 } END { exit !found }' /proc/mounts; then need /boot 150; fi
exit $fail
`
	return Step{Text: "check there is room: 2 GiB on / and for the packages, 150 MiB on a separate /boot", Script: script}
}
