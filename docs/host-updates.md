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
