# Architecture

```
            ┌───────────── nepfleet-ui (static SPA, optional) ─────────────┐
            │      talks to any number of backends over HTTPS              │
            └──────────────┬───────────────────────────────┬───────────────┘
                           │                               │
                 ┌─────────▼─────────┐           ┌─────────▼─────────┐
   fleet repo ──►│ nepfleet server   │           │ nepfleet server   │◄── fleet repo
   (git)         │  (home backend)   │◄─ link ──►│  (cloud backend)  │    (git)
                 └──┬─────────────┬──┘ WireGuard └──┬─────────────┬──┘
              mTLS  │             │                 │             │
             ┌──────▼───┐   ┌─────▼────┐      ┌─────▼────┐  ┌─────▼────┐
             │  agent   │   │  agent   │      │  agent   │  │  agent   │
             │  host A  │   │  host B  │      │ swarm n1 │  │ swarm n2 │
             └──────────┘   └──────────┘      └──────────┘  └──────────┘
```

## Components

The `nepfleet` binary runs in three roles.

| Role | Runs on | Does |
|---|---|---|
| **CLI** | Your machine or CI | `init`, `plan`, `apply`, `rollback`, `import`, `template validate` |
| **server** | One host per backend | Owns the fleet repository, reconciles it with the hosts and serves the optional API |
| **agent** | Every managed host | Talks to the local Docker Engine and runs `docker compose` / `docker stack`. Optionally manages host OS updates. |

A *backend* is one server plus its agents. On a single host, server and agent run in the same process.

## Reconcile loop

1. The desired state is the `HEAD` commit of the fleet repository. Changes arrive through `git push`, the CLI or the API, and the API writes commits too.
2. The server renders each stack: template plus values plus decrypted secrets, giving the final compose file.
3. The server diffs that against what the agent reports and produces a **plan**.
4. The plan is applied automatically or after confirmation (per stack: `apply: auto | manual`). Image tags are resolved to digests and recorded in the commit.
5. The server waits for health checks. If they fail, it re-applies the previous commit.

Rollback is the same path: choose an older commit for one stack, then plan and apply. Volume *data* is not in git. Migrations that change data take a backup first (see [template spec](spec/template.md#migrations)).

## Connectivity

- **Agents dial the server** over mTLS with certificates from the server's built-in CA. Agents behind NAT need no open ports. Enrolment uses a one-time join token.
- **Backends link to each other** over WireGuard, for example home ↔ cloud. Links are declared in `tunnels/` in the fleet repository.
- **Exposing a home service publicly** uses frp: `frps` on a cloud host, `frpc` at home, tunnelled through the WireGuard link. Caddy on the cloud side terminates TLS.

## System stacks

These are installed from the `system` tier of [nepfleet-templates](https://github.com/Serraniel/nepfleet-templates). Each one is opt-in, and each one is a normal stack, so it is versioned and can be rolled back like any app.

| Need | Default | Integration |
|---|---|---|
| TLS ingress | Caddy (`caddy-docker-proxy`, built with DNS-provider modules) | `caddy.*` labels |
| Logs | Dozzle (agent mode on every host) | automatic |
| Uptime | Uptime Kuma + AutoKuma | `kuma.*` labels |
| Dashboard | Homepage | `homepage.*` labels |
| Image updates | [neptide](https://github.com/Serraniel/neptide) (Compose and Swarm, health-gated, with rollback) | `neptide.*` labels; see [decisions](decisions.md#d6-image-updates-neptide) |
| Foreign architectures | `tonistiigi/binfmt` (QEMU) | installed when an image lacks the host's architecture |
| Backups | restic to any rclone remote | `spec.volumes[].backup` |
