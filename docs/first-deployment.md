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
nepfleet enable               # lists the features and which are on
nepfleet enable ingress       # Caddy: ACME e-mail, DNS provider, token (hidden)
nepfleet enable logs          # Dozzle
nepfleet enable uptime        # Uptime Kuma
nepfleet enable dashboard     # Homepage
nepfleet enable backups       # volume-backup
nepfleet enable updates       # neptide
```

`nepfleet enable <feature>` is `nepfleet new <template>` with the feature's system template. It takes the same flags (`--set`, `--target`), and it refuses a second stack for the same feature unless you give it a name, for example Dozzle on another host: `nepfleet enable logs dozzle-nas --target nas`.

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

Small config folders can move into the fleet repository, so they're versioned and roll back with the stack. The import names the bind mounts that fit:

```sh
nepfleet import compose homepage --copy-config /home/me/docker/homepage
```

- **What moves:** the files go to `stacks/<name>/files/config/`, and the mount points at the rendered copy. They render exactly as they were. Values that look secret, such as `api_key: …`, are stored encrypted as `${CONFIG_API_KEY}`; the import lists them, so check them.
- **What stays out:** binary files, links, and folders over the limits of [config files](spec/fleet-repo.md).
- **What changes:** the mount does, so the stack isn't adopted. The next apply recreates its containers, as a first deploy with no automatic rollback. The old folder stays where it is.
- **Apps that write there:** each apply overwrites their changes, so the import warns about mounts that aren't read-only. Leave out folders the app keeps changing.
- `import portainer` doesn't offer this yet.

Stacks Portainer manages keep their compose files in Portainer's data volume, out of reach for `import compose`; `import portainer` reads them and their environment through the Portainer API instead, exactly as Portainer passed them to compose. Create an access token in Portainer (My account → Access tokens) and pass it as `PORTAINER_TOKEN` or at the prompt, never as a flag. With several Portainer environments, pass `--endpoint` with this host's (the listing shows them); only stacks running here are adopted. Relative bind mounts keep the host path Portainer gave the daemon (`/data/compose/<id>/…`), so they keep their data. Stacks with additional compose files or deployed from git are refused; create those in the fleet by hand from their files. Portainer's default certificate is self-signed: pass it with `--ca-file`. Afterwards leave those stacks alone in Portainer, since deleting one there removes its containers.

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

Single host only. There is no web UI yet. Host OS updates, Swarm, tunnels and scheduled maintenance announcements are planned; see the [roadmap](roadmap.md). Before an apply recreates services of a running stack, `nepfleet apply` stops all its containers and copies their volumes (each volume once; the copies are deleted after a healthy apply); if the new version fails its health check, the data is restored before the previous version starts (a data-safe rollback). Large media volumes stay out with `snapshot: false` in the template or `snapshotExclude` in `stack.yaml`; `spec.snapshot: off` or `apply --no-snapshot` skips it. Bind mounts are not copied, so keep `volume-backup` enabled.
