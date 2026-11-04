# Install

NepFleet runs as a native binary or as a container. Both use the same fleet repository, so you can switch between them later.

| | Native (systemd) | Container |
|---|---|---|
| Stacks, updates, rollback | ✓ | ✓ |
| Swarm (on a manager node) | ✓ | ✓ |
| Host OS updates and reboots | ✓ | ✗ (planned: a privileged host helper) |
| Install effort | binary + unit file | `docker compose up -d` |

## Container

```sh
sudo mkdir -p /srv/nepfleet && cd /srv/nepfleet
age-keygen -o age.key                      # keep a copy of this key outside the host
docker run --rm -v "$PWD:/srv/nepfleet" -w /srv/nepfleet ghcr.io/serraniel/nepfleet init fleet
curl -fsSLO https://raw.githubusercontent.com/Serraniel/nepfleet/main/compose.yaml
DOCKER_GID=$(getent group docker | cut -d: -f3) docker compose up -d
```

Things to know:

- **Mount paths must match.** NepFleet calls `docker compose` with the host's Docker daemon, and the daemon resolves bind mounts on the host. That is why `compose.yaml` mounts `/srv/nepfleet` at the same path inside the container.
- **The Docker socket is root access.** The container is read-only and unprivileged and reaches Docker only through group membership. Never expose port 7480 directly; put the Caddy system stack in front of it.
- **Self-updates** hand off to a short-lived helper container, because a container cannot replace itself mid-update.

## Native

```sh
go install github.com/Serraniel/nepfleet/cmd/nepfleet@latest   # or download a signed release binary
nepfleet init /srv/nepfleet/fleet
```

A systemd unit and `nepfleet host bootstrap` will automate this setup (milestone v0.2).
