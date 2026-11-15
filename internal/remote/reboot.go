// SPDX-License-Identifier: AGPL-3.0-or-later

package remote

import (
	"errors"
	"fmt"
	"time"
)

// RebootWait is how long a reboot plan waits for the machine to come back
// with Docker running and its containers healthy.
const RebootWait = 10 * time.Minute

// bootIDFile keeps the boot before the reboot, in the SSH user's home, so
// the check afterwards knows the machine did restart.
const bootIDFile = `"$HOME/.cache/nepfleet-boot-id"`

// RebootPlan reboots a machine and checks it afterwards: the boot changed,
// Docker runs, and no container is starting or unhealthy (docs/host-updates.md).
// The reboot is scheduled a few seconds out, so the SSH command that asks for
// it returns cleanly.
func RebootPlan(r *Report) ([]Step, error) {
	if r.Kernel != "Linux" {
		return nil, fmt.Errorf("NepFleet reboots Linux hosts; this is %s", r.Kernel)
	}
	if !r.Root && !r.Sudo && !r.SudoPassword {
		return nil, errors.New("rebooting needs root: log in as root, or as a user who may use sudo")
	}
	return []Step{
		{Text: "note the current boot, to tell afterwards that the machine restarted",
			Script: "mkdir -p \"$HOME/.cache\"\ncat /proc/sys/kernel/random/boot_id > " + bootIDFile + "\n"},
		{Text: "reboot in 5 seconds (the stacks stop, and start again with Docker)", Root: true,
			Script: "if command -v systemd-run >/dev/null 2>&1; then\n" +
				"  systemd-run --quiet --on-active=5 --timer-property=AccuracySec=1s systemctl reboot\n" +
				"else\n" +
				"  nohup sh -c 'sleep 5; reboot' >/dev/null 2>&1 &\n" +
				"fi\n"},
		{Text: fmt.Sprintf("wait until it is back (up to %s), with Docker running and no container starting or unhealthy", RebootWait),
			Wait: RebootWait,
			Script: "test -f " + bootIDFile + " || { echo 'the boot before the reboot was not noted' >&2; exit 1; }\n" +
				"if [ \"$(cat /proc/sys/kernel/random/boot_id)\" = \"$(cat " + bootIDFile + ")\" ]; then echo 'not restarted yet' >&2; exit 1; fi\n" +
				"docker info >/dev/null 2>&1 || { echo 'Docker is not running yet' >&2; exit 1; }\n" +
				"waiting=$(docker ps --filter health=starting --filter health=unhealthy --format '{{.Names}}')\n" +
				"if [ -n \"$waiting\" ]; then echo \"not healthy yet: $(echo $waiting)\" >&2; exit 1; fi\n" +
				"rm -f " + bootIDFile + "\n"},
	}, nil
}
