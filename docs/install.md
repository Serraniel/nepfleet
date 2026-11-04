# Install

NepFleet runs as a native binary or as a container. Both use the same fleet repository, so you can switch between them later.

| | Native (systemd) | Container |
|---|---|---|
| Stacks, updates, rollback | ✓ | ✓ |
| Swarm (on a manager node) | ✓ | ✓ |
| Host OS updates and reboots | ✓ | ✗ (planned: a privileged host helper) |
| Install effort | binary + unit file | `docker compose up -d` |

## Container

The host needs only Docker with the Compose plugin, plus git to commit and push the fleet repository. The image ships git, sops and age, so the CLI, secrets and key generation all run in the container.

```sh
sudo mkdir -p /srv/nepfleet && sudo chown "$(id -u):$(id -g)" /srv/nepfleet && cd /srv/nepfleet
mkdir fleet state     # create them yourself, or Docker creates them owned by root
curl -fsSLO https://raw.githubusercontent.com/Serraniel/nepfleet/main/compose.yaml
cat > .env <<EOF
NEPFLEET_UID=$(id -u)
NEPFLEET_GID=$(id -g)
DOCKER_GID=$(getent group docker | cut -d: -f3)
NEPFLEET_HOST=$(hostname)
EOF
```

Run the CLI through Compose, so it gets the same mounts and settings as the API. Put this function in your `~/.bashrc` or `~/.zshrc`:

```sh
nepfleet() {
  if [ -t 0 ]; then set -- nepfleet "$@"; else set -- -T nepfleet "$@"; fi   # -T when piped
  docker compose -f /srv/nepfleet/compose.yaml run --rm "$@"
}
```

Create the key and the fleet repository:

```sh
docker compose run --rm --entrypoint age-keygen nepfleet -o /srv/nepfleet/age.key
docker compose run --rm --entrypoint age-keygen nepfleet -y /srv/nepfleet/age.key   # prints the public key
nepfleet init .       # runs in /srv/nepfleet/fleet; then add the public key to fleet.yaml
nepfleet doctor
```

Keep a copy of `age.key` outside the host: without it, every secret in the repository is unreadable. Then continue with [first deployment](first-deployment.md) at step 3. Every `nepfleet` command there works through the function. Edit and commit the fleet repository on the host with your own git.

Start the API when you want it:

```sh
docker compose up -d
```

Things to know:

- **Mount paths must match.** NepFleet calls `docker compose` with the host's Docker daemon, and the daemon resolves bind mounts on the host. That is why `compose.yaml` mounts `/srv/nepfleet` at the same path inside the container, and keeps the state directory (`/srv/nepfleet/state`) there.
- **It runs as you.** The container runs as `NEPFLEET_UID:NEPFLEET_GID`, so the files it writes stay editable on the host.
- **The Docker socket is root access.** The container is read-only and unprivileged and reaches Docker only through group membership. Never expose port 7480 directly; put the Caddy system stack in front of it.
- **`doctor` sees less from a container.** It cannot see host processes or Docker's data directory, so the port and disk checks show ⚠ with a command to run on the host instead.
- **Rollback commits** use `NEPFLEET_GIT_NAME` and `NEPFLEET_GIT_EMAIL` from `.env` (default `NepFleet <nepfleet@localhost>`) and are not signed.
- **Self-updates** hand off to a short-lived helper container, because a container cannot replace itself mid-update.

## Native

```sh
go install github.com/Serraniel/nepfleet/cmd/nepfleet@latest   # or download a signed release binary
nepfleet init /srv/nepfleet/fleet
```

A systemd unit and `nepfleet host bootstrap` will automate this setup (milestone v0.2).
