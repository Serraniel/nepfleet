# Install

NepFleet runs as a native binary or as a container. Both use the same fleet repository, so you can switch between them later.

| | Native (systemd) | Container |
|---|---|---|
| Stacks, updates, rollback | ✓ | ✓ |
| Swarm (on a manager node) | ✓ | ✓ |
| Host OS updates and reboots | ✓ | ✗ (planned: a privileged host helper) |
| Install effort | binary + unit file | `docker compose up -d` |

## Container

The host needs only Docker with the Compose plugin, git to commit and push the fleet repository, and curl for the download. The image ships git, sops and age, so the CLI, secrets and key generation all run in the container.

```sh
sudo mkdir -p /srv/nepfleet && sudo chown "$(id -u):$(id -g)" /srv/nepfleet && cd /srv/nepfleet
mkdir fleet state     # create them yourself, or Docker creates them owned by root
curl -fsSLO https://raw.githubusercontent.com/Serraniel/nepfleet/main/compose.yaml
cat > .env <<EOF
NEPFLEET_UID=$(id -u)
NEPFLEET_GID=$(id -g)
DOCKER_GID=$(getent group docker | cut -d: -f3)
NEPFLEET_HOST=$(uname -n)
EOF
```

Run the CLI through Compose, so it gets the same mounts and settings as the API. Put this function in your `~/.bashrc` or `~/.zshrc`:

```sh
nepfleet() {
  # A TTY only when both ends are a terminal, so pipes and redirects get clean output.
  if [ -t 0 ] && [ -t 1 ]; then set -- nepfleet "$@"; else set -- -T nepfleet "$@"; fi
  docker compose -f /srv/nepfleet/compose.yaml run --rm "$@"
}
```

Create the key and the fleet repository:

```sh
docker compose -f /srv/nepfleet/compose.yaml run --rm --entrypoint age-keygen nepfleet -o /srv/nepfleet/age.key
docker compose -f /srv/nepfleet/compose.yaml run --rm --entrypoint age-keygen nepfleet -y /srv/nepfleet/age.key   # prints the public key
nepfleet init .       # runs in /srv/nepfleet/fleet; then add the public key to fleet.yaml
nepfleet doctor
```

Keep a copy of `age.key` outside the host: without it, every secret in the repository is unreadable. Push the fleet repository to a private remote, such as your Forgejo or a private GitHub repository: the remote is the backup of every configuration. Then continue with [first deployment](first-deployment.md) at step 3. The `nepfleet` commands there work through the function; edit, commit and push the fleet repository on the host with your own git.

Start the API when you want it:

```sh
docker compose -f /srv/nepfleet/compose.yaml up -d
```

Things to know:

- **Mount paths must match.** NepFleet calls `docker compose` with the host's Docker daemon, and the daemon resolves bind mounts on the host. That is why `compose.yaml` mounts `/srv/nepfleet` at the same path inside the container, and keeps the state directory (`/srv/nepfleet/state`), the template cache and registry logins there. For a private registry, log in once with `docker compose -f /srv/nepfleet/compose.yaml run --rm --entrypoint docker nepfleet login ghcr.io`.
- **It runs as you.** The container runs as `NEPFLEET_UID:NEPFLEET_GID`, so the files it writes stay editable on the host.
- **The Docker socket is root access.** The container is read-only and unprivileged and reaches Docker only through group membership. Never expose port 7480 directly; put the Caddy system stack in front of it.
- **Emulators are read from `/host/binfmt_misc`.** `compose.yaml` mounts the host's `binfmt_misc` read-only there, so `apply` knows whether an image built for another CPU runs under emulation (the `binfmt` system stack). Without the mount, such images are only warned about.
- **`doctor` sees less from a container.** It cannot see host processes or Docker's data directory, so the port and disk checks show ⚠ with a command to run on the host instead.
- **Rollback commits** use `NEPFLEET_GIT_NAME` and `NEPFLEET_GIT_EMAIL` from `.env` (default `NepFleet <nepfleet@localhost>`) and are not signed.
- **`import compose` only sees projects under `/srv/nepfleet`.** It reads each project's files, and the container sees only that mount. To adopt a project elsewhere, add its directory to `volumes:` at the same path, or run the import natively.
- **Self-updates are planned** ([#17](https://github.com/Serraniel/nepfleet/issues/17)): a short-lived helper container will recreate NepFleet, because a container cannot replace itself mid-update. Until then, `docker compose pull && docker compose up -d`.

### Upgrading from the earlier compose.yaml

The earlier file ran as uid 65532 and read the age key as a Compose secret. The new one runs as you, so hand the files to your user once and add the `.env` above:

```sh
sudo chown -R "$(id -u):$(id -g)" /srv/nepfleet
```

## Native

```sh
go install github.com/Serraniel/nepfleet/cmd/nepfleet@latest   # or download a signed release binary
nepfleet init /srv/nepfleet/fleet
```

A systemd unit and `nepfleet host bootstrap` will automate this setup (milestone v0.2).
