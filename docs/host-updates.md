# Host updates (opt-in)

Host updates are off by default. Enable them per host in `hosts/<name>.yaml`:

```yaml
spec:
  updates:
    enabled: true
    schedule: "Sun 03:00"       # regular updates
    security: "daily 04:00"     # security fixes, applied sooner
    reboot: when-required       # never | when-required | always
```

## How a host update runs

1. **Pre-checks**: there must be enough free disk space, no other host in the same cluster may be in maintenance, and the OS-specific checks below must pass.
2. **Drain**: on Swarm, the node is drained following the [rolling procedure](swarm.md#rolling-operations). On Compose, stacks keep running.
3. **Update**, then **reboot** if a reboot is required.
4. **Verify**: the agent reconnects, Docker is healthy and every stack passes its health checks. If verification fails, the update stops there and you are notified. It never continues to the next node.

## Distributions

| OS | Status | Update | Reboot required when | Security fixes |
|---|---|---|---|---|
| Arch Linux | first | `pacman -Syu` after updating `archlinux-keyring` | the running kernel's modules are gone from `/usr/lib/modules` | `arch-audit` flags a vulnerable package |
| Debian / Ubuntu | planned | `apt-get dist-upgrade` | `/var/run/reboot-required` exists | security pocket via `unattended-upgrades` |
| Fedora / RHEL | planned | `dnf upgrade` | `dnf needs-restarting -r` | `dnf upgrade --security` |
| openSUSE | later | `zypper dup` / `patch` | `zypper needs-rebooting` | `zypper patch --category security` |

**Arch-specific safeguard:** before updating, the agent reads the [Arch news feed](https://archlinux.org/feeds/news/). If a news item has appeared since the last update, the host is not updated and you are notified, because those items usually announce manual interventions.

## Checking for pending updates

Before anything installs updates, the desktop app can show what each machine has pending: Fleets → **Check host updates** (the control daemon's `POST /v1/remotes/<name>/host-updates`). It runs over SSH as the connecting user and installs nothing:

- **Arch Linux:** `checkupdates` (pacman-contrib) syncs a package database of its own, so the count is fresh; without it, `pacman -Qu` reads the database as of the last `pacman -Sy`. Security fixes come from `arch-audit --upgradable` when it's installed. A reboot is required when the running kernel's modules are gone.
- **Debian / Ubuntu:** `apt list --upgradable`, from the package lists as of the last `apt update`; security fixes are the ones from a `-security` suite; a reboot is required when `/var/run/reboot-required` exists.
- **Fedora / RHEL:** `dnf check-update`, `dnf updateinfo list --security` and `dnf needs-restarting -r`.

It answers `{os, supported, pending, security, rebootRequired, packages, notes, lastUpgrade, news}`: `security` is `-1` and `rebootRequired` is `null` when the machine can't tell, and `notes` says how fresh or complete the answer is.

**Arch news:** on Arch and its derivatives, `lastUpgrade` is when the last full upgrade started (from `/var/log/pacman.log`), and `news` lists the [Arch news](https://archlinux.org/news/) items published since. Read them before updating: they usually announce manual steps. The control daemon reads the feed from archlinux.org, at most once an hour.

## Updating a machine from the app

Fleets → **Update the host…** plans the update for review, like setting a machine up, and runs it only when you start it (setup kind `host-update`). It needs root or sudo, as preparing the machine does, and never reboots: when the check says a reboot is required, reboot it yourself when it suits.

- **Arch:** `pacman -Sy archlinux-keyring` first, then `pacman -Su`: never a partial upgrade. Read the Arch news the check lists before you start it.
- **Debian / Ubuntu:** `apt-get update`, then `apt-get dist-upgrade`, keeping config files you changed.
- **Fedora:** `dnf upgrade --refresh`.

An upgrade of Docker itself restarts Docker, and with it every container on the machine.

## Rebooting a machine from the app

When the check says a reboot is required, Fleets offers **Reboot…**, a plan to review like the others (setup kind `reboot`):

1. It notes the current boot (in the SSH user's `~/.cache`), to tell afterwards that the machine did restart.
2. It reboots 5 seconds later, as root (`systemd-run`, else a background `sleep 5; reboot`), so the SSH command returns cleanly.
3. It waits up to 10 minutes, trying every 10 seconds, until the machine answers with a new boot, Docker runs, and no container is starting or unhealthy. If that doesn't happen, the job fails and names what wasn't ready.

The stacks stop with the machine and start again with Docker (their restart policies). On a Swarm node, drain it first: rolling node maintenance is planned (#54).
