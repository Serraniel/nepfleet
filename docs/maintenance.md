# Maintenance windows (Uptime Kuma)

While NepFleet changes a stack, its monitors are put into **maintenance** in Uptime Kuma. A maintenance window doesn't alert, shows "Maintenance" instead of "Down", and appears on status pages.

```yaml
# fleet.yaml
spec:
  uptimeKuma:
    - name: global
      url: https://status.example.org
      username: admin
      passwordSecret: KUMA_PASSWORD     # key in fleet.secret.yaml
```

```sh
nepfleet secret set --global KUMA_PASSWORD
```

List one entry per Kuma, for example a global instance and a stack's own instance.

## When a window is opened

- `apply` changes a stack's compose file, env file or image pins, and the stack has AutoKuma monitors (`kuma.<id>.<type>.name` labels). The window covers exactly those monitors.
- It opens before `docker compose up` and closes after the health gate passes or the rollback finishes. It is scheduled to expire by itself (health-gate timeout + 10 minutes), so monitors never stay silenced if NepFleet is killed.
- Monitors are matched by their AutoKuma id (the `AutoKuma` tag), not by name. A name is used only if exactly one monitor has it, so the same template on another host is never silenced.
- A Kuma that is unreachable or misconfigured only produces a warning. It never blocks a deployment.
- `nepfleet.maintenance: "false"` on a service keeps its monitors out of windows.

## Rolling updates in a swarm

A swarm update where every monitored service runs two or more replicas, or runs globally, is not downtime: replicas are replaced one at a time. What each Kuma does then is set by `rollingUpdates` on its entry:

```yaml
  uptimeKuma:
    - name: global
      url: https://status.example.org
      rollingUpdates: incident     # incident (default) | maintenance | none
```

- **`incident`:** a warning incident ("Rolling update of <stack>") goes on every status page that shows one of the stack's monitors, and it is resolved when the update is done or rolled back. Only that incident is resolved, so incidents you wrote stay. This needs Uptime Kuma 2.1 or newer; 2.0.x cannot resolve a single incident, so it gets a warning and no incident. If NepFleet is killed during the update, the incident stays until you resolve it in Kuma.
- **`maintenance`:** a maintenance window, as for every other update.
- **`none`:** nothing.

`plan` shows which of the three applies. These always get a maintenance window:

- the first deployment of a stack and compose stacks;
- a monitored service with a single replica, or with a replica count NepFleet can't read (`${VAR}`);
- an `update_config` that replaces all replicas at once: `parallelism: 0`, or stop-first with `parallelism` at least the replica count.

Planned: host update windows announced in advance, and per-node windows for drains (see the [roadmap](roadmap.md)).
