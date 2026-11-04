# NepFleet

Manage Linux servers and Docker deployments from one git repository.

- **Git is the source of truth.** Every stack, volume definition, tunnel and host setting lives in a *fleet repository*. Every change is a commit, so you can roll back a single stack or move the whole fleet to a new backend.
- **File-first.** You can do everything by editing files and running `nepfleet apply`. The [web UI](https://github.com/Serraniel/nepfleet-ui) is optional and can connect to several backends at once.
- **Compose and Swarm.** NepFleet runs on single hosts with Compose and on clusters with Docker Swarm. Swarm clusters get rolling updates, ingress and shared storage.
- **Batteries included, not reinvented.** NepFleet sets up proven tools for you: Caddy for TLS, Dozzle for logs, Uptime Kuma for uptime, frp and WireGuard for tunnels, and SOPS for secrets.
- **Safe by default.** Every change shows a plan before it is applied. Updates roll back on failed health checks. The API is off until you enable it.

> **Status: early development.** Single-host Compose stacks work end to end: template rendering, SOPS secrets, plan, health-gated apply with automatic rollback, history and git rollback. Swarm, multi-host, the API and host updates are planned in [milestones](https://github.com/Serraniel/nepfleet/milestones).

## Quick start

```sh
go install github.com/Serraniel/nepfleet/cmd/nepfleet@latest
nepfleet init ~/fleet && cd ~/fleet    # add your age public key to fleet.yaml
mkdir -p stacks/vault && cat > stacks/vault/stack.yaml <<'YAML'
apiVersion: nepfleet/v1alpha1
kind: Stack
metadata: {name: vault}
spec:
  target: local
  template: {source: official, name: vaultwarden}
  values: {DOMAIN: vault.example.org}
YAML
nepfleet plan                          # what would change (secrets masked)
nepfleet apply                         # health-gated; rolls back if unhealthy
nepfleet history vault && nepfleet rollback vault
```

Or run it as a container: `ghcr.io/serraniel/nepfleet:latest` (`:edge` for the newest build of `main`). See [install](docs/install.md) and [compose.yaml](compose.yaml).

## Repositories

| Repo | Purpose |
|---|---|
| [nepfleet](https://github.com/Serraniel/nepfleet) | Backend, agent and CLI (this repo) |
| [nepfleet-ui](https://github.com/Serraniel/nepfleet-ui) | Optional web frontend for one or more backends |
| [nepfleet-templates](https://github.com/Serraniel/nepfleet-templates) | Official templates for system services and apps |
| [neptide](https://github.com/Serraniel/neptide) | Health-gated image updates for Compose and Swarm, standalone or as NepFleet's update engine |

## Documentation

- [Install](docs/install.md): native or as a container
- [Architecture](docs/architecture.md): components and how they interact
- [Decisions](docs/decisions.md): what we chose and why
- [Fleet repository](docs/spec/fleet-repo.md) and [template format](docs/spec/template.md)
- [Security model](docs/security.md)
- [Swarm: storage, ingress, DNS](docs/swarm.md)
- [Host updates](docs/host-updates.md)
- [Notifications](docs/notifications.md): Discord, Slack, Telegram, ntfy, webhooks …
- [Releases](docs/releases.md) and [supply chain / SBOMs](docs/supply-chain.md)

## License

[AGPL-3.0-or-later](LICENSE). You may use NepFleet commercially. If you distribute a modified version or offer it over a network, you must publish your changes under the same license.
