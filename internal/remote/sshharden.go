// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"errors"
	"fmt"
)

// sshHardenFile is NepFleet's drop-in for sshd. Early in the order: sshd
// takes the first value it reads, and distributions put their own (Ubuntu's
// 50-cloud-init.conf turns passwords on) later.
const sshHardenFile = "/etc/ssh/sshd_config.d/10-nepfleet.conf"

// SSHHardenPlan turns off SSH password logins, and root logins except with
// a key when this connection is root's (security check: ssh-password,
// ssh-root). The connection that plans it already logs in with a key; each
// later step is a new login, so the last one proves keys still work.
func SSHHardenPlan(r *Report) ([]Step, error) {
	if r.Kernel != "Linux" {
		return nil, fmt.Errorf("NepFleet changes sshd on Linux hosts; this is %s", r.Kernel)
	}
	if !r.Root && !r.Sudo && !r.SudoPassword {
		return nil, errors.New("changing sshd needs root: log in as root, or as a user who may use sudo")
	}
	root := "no"
	if r.Root {
		// Never lock out the login this connection uses.
		root = "prohibit-password"
	}
	conf := "# Written by NepFleet (Fleets: Turn off SSH password logins). Remove this file to undo.\n" +
		"PasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin " + root + "\n"
	return []Step{
		{Text: "turn off password logins and " + map[bool]string{true: "root logins with a password", false: "root logins"}[r.Root] +
			" in " + sshHardenFile + ", check the config (sshd -t) and reload sshd; open sessions stay", Root: true,
			Script: "grep -qiE '^[[:space:]]*Include[[:space:]]+/etc/ssh/sshd_config\\.d/\\*\\.conf' /etc/ssh/sshd_config || " +
				"{ echo 'sshd_config does not include /etc/ssh/sshd_config.d/*.conf: add PasswordAuthentication no there yourself' >&2; exit 1; }\n" +
				"mkdir -p /etc/ssh/sshd_config.d\n" +
				"prev=$(mktemp)\n" +
				"if [ -f " + sshHardenFile + " ]; then cp -p " + sshHardenFile + " \"$prev\"; else : > \"$prev\"; fi\n" +
				"printf '%s' " + shQuote(conf) + " > " + sshHardenFile + "\n" +
				"chmod 644 " + sshHardenFile + "\n" +
				"sshd=$(command -v sshd || echo /usr/sbin/sshd)\n" +
				"if ! \"$sshd\" -t; then\n" +
				"  if [ -s \"$prev\" ]; then cp -p \"$prev\" " + sshHardenFile + "; else rm -f " + sshHardenFile + "; fi\n" +
				"  echo 'sshd -t refused the config; nothing changed' >&2; exit 1\n" +
				"fi\n" +
				"rm -f \"$prev\"\n" +
				"\"$sshd\" -T 2>/dev/null | grep -qx 'passwordauthentication no' || { echo 'another sshd setting still allows passwords (it comes first): check sshd -T' >&2; exit 1; }\n" +
				"systemctl reload sshd 2>/dev/null || systemctl reload ssh\n"},
		{Text: "log in again with your key, to check it still works",
			Script: "true\n"},
	}, nil
}
