"""Product requirements (from the original brief and follow-ups) mapped to issues.

Each requirement lists (repo, issue title) pairs; roadmap.py resolves them to
links and states. "doc:" entries point at documentation instead of an issue.
"""

R = [
    # --- Product shape
    ("Product", "One interface to manage servers and Docker deployments, with easy rollbacks", [
        ("nepfleet", "`nepfleet apply`: deploy stacks with docker compose"),
        ("nepfleet", "`nepfleet rollback <stack> [commit]`"),
        ("nepfleet", "Health-gated apply with automatic rollback")]),
    ("Product", "Fully usable file-based, without the web UI", [
        ("doc", "docs/spec/fleet-repo.md"), ("nepfleet", "`nepfleet plan` output format (human and JSON)")]),
    ("Product", "Optional, modern web UI with dark and light mode", [
        ("nepfleet-ui", "Design system: components and tokens"),
        ("nepfleet-ui", "Theme: follow the system setting, with an explicit override")]),
    ("Product", "One frontend for several backends (home and cloud), backends can be linked", [
        ("nepfleet-ui", "Stack list across all backends"),
        ("nepfleet", "Linked backends: one UI over home and cloud backends"),
        ("nepfleet", "WireGuard links between hosts and backends (tunnels/*.yaml)")]),
    ("Product", "Secure enough to expose to the internet", [
        ("nepfleet", "Serve the API over TLS (ACME or provided certificates)"),
        ("nepfleet", "Authentication: passkeys (WebAuthn) with TOTP fallback"),
        ("nepfleet", "Threat model review before exposing the API"),
        ("nepfleet", "Repository hardening: rulesets, CodeQL, OpenSSF Scorecard")]),
    ("Product", "Minimise the risk of destroying things", [
        ("doc", "docs/decisions.md#d4-plan-before-apply"),
        ("nepfleet-ui", "Plan dialog before every apply")]),
    ("Product", "Backend deployable with Docker", [("nepfleet", "Run NepFleet as a container (compose.yaml)"),
        ("nepfleet", "Containerized self-update via a hand-off helper")]),
    # --- Host OS
    ("Host OS", "Easy setup of fresh servers without Docker", [("nepfleet", "`nepfleet host bootstrap`: prepare a fresh server over SSH")]),
    ("Host OS", "Adopt existing servers: running containers, compose projects, swarm, Portainer", [
        ("nepfleet", "Import running Compose projects"), ("nepfleet", "Import stacks from Portainer"),
        ("nepfleet", "Import existing Swarm stacks and services"), ("nepfleet-ui", "Adopt running stacks from the UI (import wizard)")]),
    ("Host OS", "Opt-in host OS updates with reboot, schedule, faster security fixes; Arch first", [
        ("nepfleet", "Arch Linux: scheduled updates with safeguards"),
        ("nepfleet", "Arch Linux: fast path for security fixes via arch-audit"),
        ("nepfleet", "Reboot orchestration and post-reboot verification")]),
    ("Host OS", "Rolling host updates across a swarm", [("nepfleet", "Rolling host updates across a swarm")]),
    ("Host OS", "Other distributions (lower priority)", [
        ("nepfleet", "Debian / Ubuntu support"), ("nepfleet", "Fedora / RHEL family support"), ("nepfleet", "openSUSE support")]),
    ("Host OS", "Ensure the host can run the images (architecture, emulation)", [
        ("nepfleet", "`nepfleet doctor`: check that a host can run what is planned"),
        ("nepfleet", "Architecture check before deploy, with optional binfmt")]),
    # --- Docker
    ("Docker", "Portainer-like stack management, every config versioned in git", [
        ("nepfleet", "Stack spec: load and validate stacks/<name>/stack.yaml"),
        ("nepfleet", "Renderer: template + values + secrets → compose.yaml + env file"),
        ("nepfleet", "Pin image digests in lock.yaml")]),
    ("Docker", "Backups of configs and volumes; move a whole fleet to a new backend", [
        ("nepfleet", "Backups: restic for volumes marked backup: true"),
        ("nepfleet", "Off-host backup of the fleet repository and age key"),
        ("nepfleet", "Move a fleet to a new backend installation")]),
    ("Docker", "Automated rolling image updates (Compose and Swarm), with rollback", [
        ("neptide", "Integration test: compose update and rollback against a local registry"),
        ("neptide", "Integration test: swarm service update and rollback in Docker-in-Docker"),
        ("nepfleet", "Automatic image updates in git (GitOps mode)")]),
    ("Docker", "Logs (Dozzle) and uptime (Uptime Kuma, global or per stack), set up automatically", [
        ("nepfleet", "Install system stacks (caddy, dozzle, uptime-kuma, homepage …) as opt-in features"),
        ("nepfleet-templates", "Uptime Kuma: per-stack status-page mode")]),
    ("Docker", "Encrypted tunnels from home to cloud (frp), config in the repo", [
        ("nepfleet", "frp exposures declared in tunnels/")]),
    ("Docker", "Automatic TLS, including trusted certificates on the home network", [
        ("nepfleet", "Docs: trusted TLS on a home network with DNS-01")]),
    ("Docker", "Notifications (Discord and common hooks)", [
        ("nepfleet", "Notifications: channels and routing via shoutrrr URLs"),
        ("nepfleet", "Notifications: rich Discord, Slack and Teams messages"),
        ("nepfleet", "Notifications: signed generic JSON webhook")]),
    # --- Swarm
    ("Swarm", "Shared storage: external shares and internal replication that survives a node", [
        ("nepfleet", "External volumes: CIFS/NFS/SFTP (e.g. Hetzner Storage Box)"),
        ("nepfleet", "Research: replicated storage for 2–5 heterogeneous nodes"),
        ("nepfleet", "Pinned volume class for databases with continuous backup")]),
    ("Swarm", "Rolling releases with minimal downtime", [
        ("nepfleet", "Rolling-update defaults (start-first, rollback on failure)"),
        ("nepfleet", "Rolling node maintenance (drain → work → verify → activate)")]),
    ("Swarm", "Ingress and DNS without routing everything through the manager", [
        ("nepfleet", "Distributed ingress: Caddy in global mode on ingress-labelled nodes"),
        ("nepfleet", "DNS record management for ingress nodes (libdns providers)")]),
    # --- Templates
    ("Templates", "Template repo with stack files, defaults, wizards and generated secrets; system vs app tier", [
        ("doc", "docs/spec/template.md"), ("nepfleet-ui", "Wizard renderer from template inputs")]),
    ("Templates", "Additional template repositories", [("nepfleet", "Template sources: fetch, pin and verify catalogues")]),
    ("Templates", "Issue automatically when an image publishes a new major version", [("doc", "https://github.com/Serraniel/nepfleet-templates/blob/main/.github/workflows/image-updates.yml")]),
    ("Templates", "Integration flags, and every template supports every applicable integration", [
        ("nepfleet-templates", "CI: `supports` flags must match the labels actually wired"),
        ("nepfleet-templates", "Integration coverage: every app wires every applicable integration")]),
    ("Templates", "Safe migrations with rollback and update paths, also user-defined", [
        ("nepfleet", "Migration engine: path finding, backups, steps, rollback"),
        ("nepfleet", "User-defined templates and migrations in the fleet repository"),
        ("nepfleet-templates", "Migration example: Postgres major upgrade (dump/restore)")]),
    ("Templates", "Wizards from foreign compose files, template hints, diffs and upstream PRs", [
        ("nepfleet", "Generate a template (wizard) from any compose file"),
        ("nepfleet", "Diff an installed or imported stack against its template"),
        ("nepfleet", "Submit a tested template as a PR to a template repository"),
        ("nepfleet-ui", "Template hints and diff for imported stacks")]),
    ("Templates", "Staples beyond the services already used", [("nepfleet-templates", "Template: Immich")]),
    # --- UI
    ("UI", "Raw YAML editing is always possible, plus a visual split view", [
        ("nepfleet-ui", "YAML editor (CodeMirror 6) with schema validation"),
        ("nepfleet-ui", "Visual split view: services, volumes, networks, variables")]),
    # --- Delivery
    ("Delivery", "Edge images and on-demand semantic releases (release-please); `sha-` tags", [("doc", "docs/releases.md")]),
    ("Delivery", "Dependabot everywhere; production updates release; security fixes in a Security section", [
        ("nepfleet", "Verify the Dependabot classifier and release flow on the first real PRs")]),
    ("Delivery", "Base images always current (digest-pinned and rebuilt)", [("doc", "docs/releases.md#dependencies")]),
    ("Delivery", "CRA-style SBOMs, signatures, vulnerability handling; infrastructure SBOMs", [
        ("doc", "docs/supply-chain.md"),
        ("nepfleet", "Infrastructure SBOM: `nepfleet sbom --target host|swarm|stack`"),
        ("nepfleet", "Advisory scanning of running images (from infrastructure SBOMs)")]),
]
