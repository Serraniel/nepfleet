# First deployment (single host)

This guide sets up one host with ingress, logs, uptime, a dashboard, backups and your apps, all driven from a fleet repository. Allow about 30 minutes. Everything here works with the CLI alone.

## 1. Install

You need Docker ≥ 25 with the Compose plugin, and git.

**As a container** (nothing else on the host): follow [install](install.md#container), which also creates the key and the fleet repository, then continue at step 3.

**Natively** you also need sops and age. On Arch Linux:

```sh
sudo pacman -S --needed docker docker-compose git sops age
sudo systemctl enable --now docker
go install github.com/Serraniel/nepfleet/cmd/nepfleet@latest
```

## 2. Create the fleet repository and its key

```sh
mkdir -p ~/.config/sops/age
age-keygen -o ~/.config/sops/age/keys.txt        # BACK THIS FILE UP OFF THE HOST
nepfleet init ~/fleet && cd ~/fleet
```

In `fleet.yaml`, add the public key (`age-keygen -y ~/.config/sops/age/keys.txt`) under `spec.secrets.age`. Then push the repository to a private remote, such as your Forgejo or a private GitHub repository. The remote is a backup of every configuration.

Without the age key, every secret in the repository is unreadable. Keep a copy in your password manager.

## 3. Check the host

```sh
nepfleet doctor
```

Fix everything marked ✗. Run it again after adding stacks: it also checks that their images exist for this CPU and that ports 80/443 are free.

## 4. System stacks

The wizard asks for each template's inputs, stores secrets encrypted and checks the values before writing anything:

```sh
nepfleet new caddy            # ACME e-mail, DNS provider, token (hidden)
nepfleet new dozzle
nepfleet new uptime-kuma
nepfleet new homepage
nepfleet new volume-backup
nepfleet new neptide
```

Each stack is just a directory with a `stack.yaml`, so you can also write it by hand. `target: local` means this host. For example, ingress:

```yaml
# system/caddy/stack.yaml
apiVersion: nepfleet/v1alpha1
kind: Stack
metadata: {name: caddy}
spec:
  target: local
  apply: auto
  template: {source: official, name: caddy}
  values:
    ACME_EMAIL: you@example.org
    CADDY_DNS_PROVIDER: cloudflare   # for trusted certificates on home-network names; or "none"
```

```sh
nepfleet secret set caddy DNS_API_TOKEN        # only with a DNS provider; prompts without echo
```

Add the others the same way. Each template's `template.yaml` lists the values it needs, and `nepfleet plan` reports any that are missing or invalid.

| Stack | Template | Values |
|---|---|---|
| `system/dozzle` | `dozzle` | `DOMAIN` |
| `system/uptime-kuma` | `uptime-kuma` | `DOMAIN`, `KUMA_USERNAME` + `secret set … KUMA_PASSWORD` |
| `system/homepage` | `homepage` | `DOMAIN` |
| `system/volume-backup` | `volume-backup` | `AGE_RECIPIENTS` (your public key); for a Storage Box `SSH_HOST_NAME`, `SSH_USER`, `SSH_HOST_PUBLIC_KEY` + `secret set … SSH_PASSWORD` |
| `system/neptide` | `neptide` | `DOCKER_GID` (`getent group docker \| cut -d: -f3`), `MONITOR_ONLY: "true"` to start safely; add the label `neptide.snapshot=true` to containers with databases, so a failed update also rolls their named volumes back (bind mounts only with `neptide.snapshot.binds=true`) |

Once Uptime Kuma runs and you have created its admin account, let NepFleet put monitors into maintenance while it changes a stack ([details](maintenance.md)):

```yaml
# fleet.yaml, under spec:
  uptimeKuma:
    - {name: global, url: "https://status.example.org", username: admin, passwordSecret: KUMA_PASSWORD}
```

```sh
nepfleet secret set --global KUMA_PASSWORD
```

## 5. Bring over what already runs

```sh
nepfleet import compose                # lists the Compose projects on this host
nepfleet import compose tdarr          # adopts one; nothing is restarted
nepfleet import portainer --url https://portainer.lan:9443   # lists Portainer's stacks
nepfleet import portainer --url https://portainer.lan:9443 nextcloud
```

Stacks Portainer manages keep their compose files in Portainer's data volume, out of reach for `import compose`; `import portainer` reads them and their environment through the Portainer API instead. Create an access token in Portainer (My account → Access tokens) and pass it as `PORTAINER_TOKEN` or at the prompt, never as a flag. Relative bind mounts are rewritten to the stack's folder in Portainer's volume (`--portainer-data`, default `/var/lib/docker/volumes/portainer_data/_data`), so they keep pointing at the existing data. Portainer's default certificate is self-signed: pass it with `--ca-file`. Afterwards leave those stacks alone in Portainer, since deleting one there removes its containers.

The import moves secrets into `values.secret.yaml` (encrypted), keeps bind mounts pointing at the existing data, and records the running containers as the applied state. The first plan is therefore empty. Read the warnings it prints.

## 6. New apps

```sh
nepfleet new vaultwarden
nepfleet new immich --set UPLOAD_PATH=/srv/photos
```

By hand, this is all it takes:

```yaml
# stacks/vaultwarden/stack.yaml
apiVersion: nepfleet/v1alpha1
kind: Stack
metadata: {name: vaultwarden}
spec:
  target: local
  template: {source: official, name: vaultwarden}
  values: {DOMAIN: vault.example.org}
```

## 7. Plan, apply, commit

```sh
nepfleet plan                 # diffs, secrets masked; ⚠ marks destructive changes
nepfleet apply                # asks per stack with apply: manual; waits for health checks
git add -A && git commit -m "feat: initial fleet" && git push
```

`apply` writes `lock.yaml` (the image digests) and stores generated secrets encrypted. Commit both. If a stack does not become healthy, `apply` restores its previous version automatically. Before it touches a stack, `apply` checks that every image is built for this host's CPU and refuses the stack otherwise. With the `binfmt` system stack installed, images for other architectures run under emulation (slower) and are only reported.

## 8. Day two

| Task | Command |
|---|---|
| See a stack's versions | `nepfleet history vaultwarden` (▶ marks the running one) |
| Roll back | `nepfleet rollback vaultwarden [commit]`: restores, commits, applies |
| Update images | `nepfleet plan --update-lock`, then `nepfleet apply --update-lock` |
| Check secrets before pushing | `nepfleet secret check` (also enforced by the pre-commit hook) |

## Known limits of this version

Single host only. There is no web UI yet. Host OS updates, Swarm, tunnels, scheduled maintenance announcements and data snapshots in `nepfleet apply` itself are planned; see the [roadmap](roadmap.md). `nepfleet apply` rolls back files and images, not volume data, so keep `volume-backup` enabled. For image updates, neptide with `neptide.snapshot=true` rolls back data as well.
