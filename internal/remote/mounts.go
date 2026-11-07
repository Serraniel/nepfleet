// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Serraniel/nepfleet/internal/fleet"
)

const mountStateDir = "/etc/nepfleet/mounts"

// UnitName is systemd's name for a path, like systemd-escape --path.
func UnitName(path, suffix string) string {
	var b strings.Builder
	for i, c := range []byte(strings.Trim(path, "/")) {
		switch {
		case c == '/':
			b.WriteByte('-')
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.' && i > 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\x%02x`, c)
		}
	}
	return b.String() + "." + suffix
}

// mountOptions are the safe defaults for containers, plus the fleet's own:
// _netdev and nofail, so a share that's away can't stop a boot; SMB 3 with
// encryption; and for CIFS the credentials outside the unit, which anyone may read.
func mountOptions(m fleet.HostMount) string {
	opts := []string{"_netdev", "nofail"}
	switch m.Type {
	case "cifs":
		opts = append(opts, "vers=3.1.1", "seal", "credentials="+mountStateDir+"/"+m.Name+".cred")
	case "nfs":
		opts = append(opts, "vers=4.2", "hard")
	case "sshfs":
		opts = append(opts, "allow_other", "reconnect", "ServerAliveInterval=15", "IdentityFile="+mountStateDir+"/"+m.Name+".key")
	}
	return strings.Join(append(opts, m.Options...), ",")
}

// MountUnit is the .mount unit.
func MountUnit(m fleet.HostMount) string {
	typ := m.Type
	if typ == "sshfs" {
		typ = "fuse.sshfs"
	}
	return fmt.Sprintf("# Written by nepfleet host apply; hosts/*.yaml is the source.\n[Unit]\nDescription=NepFleet mount %s\nWants=network-online.target\nAfter=network-online.target\n\n[Mount]\nWhat=%s\nWhere=%s\nType=%s\nOptions=%s\n", m.Name, m.Source, m.Target, typ, mountOptions(m))
}

// AutomountUnit mounts on first use, so a slow share never delays a boot.
func AutomountUnit(m fleet.HostMount) string {
	return fmt.Sprintf("# Written by nepfleet host apply.\n[Unit]\nDescription=NepFleet automount %s\n\n[Automount]\nWhere=%s\n\n[Install]\nWantedBy=multi-user.target\n", m.Name, m.Target)
}

// mountPackages installs what the mount types need.
func mountPackages(osID string, mounts []fleet.HostMount) (string, bool) {
	need := map[string]map[string]string{
		"nfs":   {"apt": "nfs-common", "pacman": "nfs-utils", "dnf": "nfs-utils"},
		"cifs":  {"apt": "cifs-utils", "pacman": "cifs-utils", "dnf": "cifs-utils"},
		"sshfs": {"apt": "sshfs", "pacman": "sshfs", "dnf": "fuse-sshfs"},
	}
	var mgr, install string
	switch osID {
	case "arch", "manjaro", "endeavouros":
		mgr, install = "pacman", "pacman -S --needed --noconfirm "
	case "debian", "ubuntu", "raspbian":
		mgr, install = "apt", "export DEBIAN_FRONTEND=noninteractive\napt-get install -y -qq "
	case "fedora":
		mgr, install = "dnf", "dnf install -y "
	default:
		return "", false
	}
	var pkgs []string
	for _, m := range mounts {
		if p := need[m.Type][mgr]; p != "" && !slices.Contains(pkgs, p) {
			pkgs = append(pkgs, p)
		}
	}
	if len(pkgs) == 0 {
		return "", true
	}
	return install + strings.Join(pkgs, " ") + "\n", true
}

// heredocMount ends file contents in scripts; values with newlines never get here.
const heredocMount = "NEPFLEET_MOUNT_EOF"

func writeFile(path, content, mode string) string {
	return fmt.Sprintf("cat > '%s' <<'%s'\n%s\n%s\nchmod %s '%s'\n", path, heredocMount, strings.TrimRight(content, "\n"), heredocMount, mode, path)
}

// MountSteps is what makes a host's mounts match its file. creds holds a
// mount's password; it reaches the remote only through the script on ssh's stdin.
func MountSteps(osID string, mounts []fleet.HostMount, creds map[string]string, present []string) ([]Step, error) {
	var steps []Step
	if len(mounts) > 0 {
		pkgs, ok := mountPackages(osID, mounts)
		if !ok {
			return nil, fmt.Errorf("installing mount tools on %q isn't supported yet: install them yourself", osID)
		}
		if pkgs != "" {
			steps = append(steps, Step{Text: "install the tools for these mount types", Root: true, Script: pkgs})
		}
	}
	want := map[string]bool{}
	for _, m := range mounts {
		want[m.Name] = true
		mountName, autoName := UnitName(m.Target, "mount"), UnitName(m.Target, "automount")
		var s strings.Builder
		s.WriteString("umask 077\nmkdir -p " + mountStateDir + "\n")
		if m.Type == "cifs" {
			pw := creds[m.Name]
			if strings.ContainsAny(pw, "\n\r") || strings.ContainsAny(m.User, "\n\r") {
				return nil, fmt.Errorf("mount %s: the user or password holds a line break", m.Name)
			}
			s.WriteString(writeFile(mountStateDir+"/"+m.Name+".cred", "username="+m.User+"\npassword="+pw+"\n", "600"))
		}
		s.WriteString("umask 022\nmkdir -p '" + m.Target + "'\n")
		s.WriteString(writeFile("/etc/systemd/system/"+mountName, MountUnit(m), "644"))
		s.WriteString(writeFile("/etc/systemd/system/"+autoName, AutomountUnit(m), "644"))
		s.WriteString(writeFile(mountStateDir+"/"+m.Name+".managed", m.Target, "644"))
		s.WriteString("systemctl daemon-reload\nsystemctl enable --now '" + autoName + "'\n")
		steps = append(steps, Step{Text: fmt.Sprintf("mount %s (%s) at %s, on first use", m.Source, m.Type, m.Target), Root: true, Script: s.String()})
	}
	// What this file managed before and doesn't list now. Only the mount goes,
	// never what's on the share or in the empty folder.
	for _, line := range present {
		name, target, ok := strings.Cut(line, " ")
		if !ok || want[name] || !fleet.ValidMountName(name) || !fleet.ValidMountTarget(target) {
			continue
		}
		mountName, autoName := UnitName(target, "mount"), UnitName(target, "automount")
		steps = append(steps, Step{Text: fmt.Sprintf("remove the mount %s at %s, which hosts/ no longer lists; data stays", name, target), Root: true,
			Script: fmt.Sprintf("systemctl disable --now '%s' || true\nsystemctl stop '%s' || true\nrm -f '/etc/systemd/system/%s' '/etc/systemd/system/%s' '%s/%s.cred' '%s/%s.managed'\nsystemctl daemon-reload\n", autoName, mountName, mountName, autoName, mountStateDir, name, mountStateDir, name)})
	}
	return steps, nil
}

// MountsPresent is a script listing what host apply set up before.
const MountsPresent = `for f in ` + mountStateDir + `/*.managed; do [ -r "$f" ] && printf '%s %s\n' "$(basename "$f" .managed)" "$(cat "$f")"; done; true
`
