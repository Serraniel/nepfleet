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
- It opens before `docker compose up` and closes after the health gate passes or the rollback finishes.
- A Kuma that is unreachable or misconfigured only produces a warning. It never blocks a deployment.
- `nepfleet.maintenance: "false"` on a service keeps its monitors out of windows.

Planned: degraded performance during Swarm rolling updates as a status-page incident, host update windows announced in advance, and per-node windows for drains (see the [roadmap](roadmap.md)).
