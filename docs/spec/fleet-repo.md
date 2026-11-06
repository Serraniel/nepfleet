# Fleet repository

`nepfleet init` creates this layout:

```
fleet.yaml                 # backend settings, template sources, update policy
.sops.yaml                 # which files are encrypted, with which age keys
hosts/
  home-nas.yaml            # address, role (compose | swarm-manager | swarm-worker), labels, host updates
stacks/
  vaultwarden/
    stack.yaml             # template ref + values, or "kind: compose" for a plain compose file
    compose.yaml           # rendered, or hand-written for plain stacks
    values.secret.yaml     # SOPS-encrypted generated/secret values
    lock.yaml              # resolved image digests (written by NepFleet)
system/                    # system stacks (caddy, dozzle, …), same layout as stacks/
tunnels/
  home-cloud.yaml          # WireGuard links and frp exposures
```

## JSON Schemas

`schemas/` in the nepfleet repository holds JSON Schemas for [`fleet.yaml`](../../schemas/fleet.schema.json), [`stack.yaml`](../../schemas/stack.schema.json) and [`template.yaml`](../../schemas/template.schema.json). They are generated from the Go types that parse these files, so they cannot drift (`go run ./tools/schema`; CI fails when they are stale), and each release attaches them. `nepfleet init`, `new` and `import` write a `# yaml-language-server: $schema=…` line, so editors with the YAML language server (VS Code, Neovim, Helix, Zed) complete and check fields as you type. `hosts/*.yaml` gets a schema once its format is implemented.

## Logging

Every service that sets no `logging:` of its own gets rotating logs, so containers cannot fill the disk: json-file with `max-size: 20m` and `max-file: 5` by default (still readable by `docker logs`, Dozzle and file-based collectors). Change it fleet-wide in `fleet.yaml` or per stack in `stack.yaml`:

```yaml
spec:
  logging:
    driver: local            # or json-file, journald, …
    options: {max-size: 50m}
    # enabled: false         # keep the daemon's defaults instead
```

Stack settings are merged over the fleet's field by field; json-file without options still gets the rotation defaults. Services that inherit `logging` through a merge key (`<<: *defaults`) keep it, services using `extends` are left alone (their base may set it), and services pulled in with a top-level `include:` are not changed. Imported stacks are adopted as they run, without these settings: their next apply recreates every service once.

## Shared folders

Name host folders that several stacks use once, in `fleet.yaml`:

```yaml
spec:
  paths:
    media: /mnt/TitanMedia                      # Jellyfin, Navidrome and Tdarr read it
    labels: /mnt/TitanData/docker/homebox/labels  # Homebox writes labels, Home Assistant prints them
```

Path inputs take `@media` instead of the path (`nepfleet new` lists the names), so stacks provably share the folder and moving a share is one change. `doctor` checks that every folder in use exists on the host.

## `stacks/<name>/stack.yaml`

```yaml
apiVersion: nepfleet/v1alpha1
kind: Stack
metadata:
  name: vaultwarden
spec:
  target: home-nas            # host name, or a swarm cluster name
  mode: compose               # compose | swarm
  apply: manual               # manual | auto
  template:                   # omit for a plain compose stack
    source: official
    name: vaultwarden
    version: 1.0.0
  values:
    DOMAIN: vault.example.org
  updates: notify             # off | notify | auto
```

A plain compose stack is a valid stack. You can paste any `compose.yaml` and NepFleet versions it just the same.

### Values from other stacks

A value can come from another stack instead of being written twice:

```yaml
  values:
    JELLYFIN_URL: {from: jellyfin, value: URL}         # a value, or the template's default for it
    JELLYFIN_KEY: {from: jellyfin, secret: API_KEY}    # a secret, generated ones included
```

- **Resolution:** references are resolved each time the stack is rendered.
- **Secrets:** a referenced secret stays in the other stack's encrypted `values.secret.yaml`. It is never copied into this stack's files in the repository, only into the rendered env file on the host, and plans mask it like the stack's own secrets.
- **Changes:** when the referenced value changes, the dependent stacks show up as changed in the next plan.
- **Order:** `plan` and `apply` handle a stack after the stacks it takes values from.
- **Cycles:** references that loop, whether stack to stack or value to value, are refused.
- **Missing values:** a generated secret exists only once its stack has been applied. Until then, a reference to it fails with a hint.

## Importing running projects

`nepfleet import compose` lists the Compose projects on the host. `nepfleet import compose <project>` adopts one as a plain stack:

- The compose file is normalised, so relative bind mounts become absolute paths and keep pointing at the existing data. `${VAR}` references are kept.
- Variables from the project's env files go to `values`. Names that look secret (`*PASSWORD*`, `*TOKEN*`, `*KEY*`, `*_URL` and so on) go encrypted to `values.secret.yaml`.
- The project name stays the same, so the first apply adopts the running containers without recreating them. CI tests this (`test/integration/import.sh`).
- Warnings cover what cannot be carried over: local builds, `env_file` references, and variables that came from the shell.

Swarm stacks and Portainer follow with their own issues.

## `lock.yaml`

`apply` pins every image to the digest it runs and writes `stacks/<name>/lock.yaml`; commit it with the stack.

- **New images** are locked to what already runs on the host, if anything does. Otherwise they are resolved from the registry. Locking therefore never changes a running image.
- **A moved tag** (for example `:1` rebuilt upstream) changes nothing until you run `nepfleet apply --update-lock`, which re-resolves every image. That is how image updates become git commits.
- **Rollback** of a stack restores its old `lock.yaml`, so exactly the old digests run again.
- **How images are pinned:** in Compose mode the digest is pulled and tagged locally, so the compose file and its config hash stay unchanged. In Swarm mode the generated stack file uses `image:tag@digest`.
- **Images that cannot be resolved,** such as local builds or private registries without credentials, run by tag. `apply` reports them.

## Copies of the key and the repository

Every secret is encrypted to the age keys in `fleet.yaml`. If you lose the private key, every secret is gone, so keep a copy somewhere other than this host:

- **`nepfleet key backup --out /media/usb/nepfleet-key.age`:** writes the key encrypted with a passphrase that `age` asks for. With `--recipient age1…`, it's encrypted to another key instead, for example one in your password manager. The copy may not be written into the fleet repository. Restore it with `age -d nepfleet-key.age > ~/.config/sops/age/keys.txt`.
- **A second recipient:** adding a second public key to `spec.secrets.age` (followed by `sops updatekeys`) also counts as a copy, because its holder can decrypt every secret.
- **`spec.mirrors`:** lists git URLs of second remotes, such as another forge or a Storage Box over SSH. `nepfleet mirror` pushes every branch and tag there and adds the mirrors as push URLs of `origin`, so a normal `git push` reaches them as well.

`nepfleet doctor` warns while it knows of no copy of the key, and while the repository has no mirror or this clone does not push to every mirror.

## Rules

- NepFleet commits only what it owns, such as `lock.yaml`, rendered output and imported stacks. Each commit message says why the change was made.
- Hand edits are fine. The next `plan` shows exactly what they change.
- Copying the repository and the age key to a new backend is a complete migration.
