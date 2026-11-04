# Decisions

These are short records of what was decided and why. To change one, open a PR against this file.

## D1 License: AGPL-3.0-or-later
Commercial use is allowed, modifications must stay under the same license, and network use counts as distribution. AGPL is the only common OSI license with all three properties. It applies to all three repositories.

## D2 Go, one static binary
The Docker, Compose and Swarm ecosystems are written in Go. Go also gives a single static binary for amd64, arm64 and armv7, and a small attack surface. NepFleet shells out to the `docker`, `docker compose` and `git` CLIs instead of re-implementing them, so the behaviour is exactly what you get by hand.

## D3 Git as the state store
The fleet repository holds all desired state. A commit is a version, a revert is a rollback, and a clone is a backup or a migration to a new backend. File-only operation needs nothing but the CLI. Secrets are committed encrypted with SOPS and age, and plaintext never touches the repository.

## D4 Plan before apply
Every mutation produces a diff first: CLI, API and UI alike. Destructive actions such as removing volumes or networks or downgrading a major version always need explicit confirmation, even when `apply: auto` is set.

## D5 Ingress and TLS: Caddy
We use `caddy-docker-proxy`, so stacks declare their routes as labels. On the public internet Caddy uses ACME HTTP-01 or TLS-ALPN. **For trusted certificates on a home network** it uses the ACME DNS-01 challenge with a real domain: nothing has to be reachable from the internet, and every device trusts the certificate. Caddy's internal CA is the fallback when you have no domain. The image is built by nepfleet-templates with the DNS-provider modules included.

## D6 Image updates: neptide
Watchtower updates containers in place without a health gate, and has no Swarm support. Upstream (`containrrr/watchtower`) was archived in December 2025. Updates therefore live in a separate project, [neptide](https://github.com/Serraniel/neptide), which is useful without NepFleet:
- Compose containers are recreated with the same config, health-gated, and the old container is restored on failure.
- Swarm services are updated by digest through Swarm's rolling update, with `failure_action: rollback`.

NepFleet uses neptide's packages. In GitOps mode it commits the new digest to the fleet repository first, so every update is also a git revision. The maintained Watchtower fork stays available as a template.

Policy per stack: `off | notify | auto`, limited by each image's `track` (patch, minor or major). Updates beyond `track` only notify.

## D7 Tunnels and linking
WireGuard (kernel) connects hosts and backends: it is fast, audited and simple. frp exposes individual services through a public host. Its configuration lives in `tunnels/` in the fleet repository and frp runs over the WireGuard link, so its traffic is always encrypted. We do not depend on Tailscale or Headscale, but either can replace the WireGuard links.

## D8 Foreign CPU architectures
Before deploying, NepFleet reads the image manifest. If the host's architecture is missing, it either installs `tonistiigi/binfmt` (QEMU user emulation, opt-in) or, on Swarm, adds a `node.platform.arch` placement constraint.

## D9 Frontend is separate and static
`nepfleet-ui` is a static SPA served from anywhere. It holds one session per backend, and each backend must list the UI's origin in `--allowed-origins`. The backend never serves HTML, which keeps its attack surface API-only.

## D10 Templates are data, not code
Templates are YAML plus a compose file. Generators, such as random secrets, are a fixed, audited set built into NepFleet. Templates cannot run arbitrary scripts on the host. Migration steps run only inside the stack's own containers.

## D11 Notifications through shoutrrr URLs
One URL format covers Discord, Slack, Telegram, Matrix, Teams, ntfy, Gotify, e-mail and webhooks. We use the maintained fork `nicholas-fedor/shoutrrr`, which Watchtower also uses. Events are routed by type, severity and stack. Details are in [notifications](notifications.md).
