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

## Importing running projects

`nepfleet import compose` lists the Compose projects on the host. `nepfleet import compose <project>` adopts one as a plain stack:

- The compose file is normalised, so relative bind mounts become absolute paths and keep pointing at the existing data. `${VAR}` references are kept.
- Variables from the project's env files go to `values`. Names that look secret (`*PASSWORD*`, `*TOKEN*`, `*KEY*`, `*_URL` and so on) go encrypted to `values.secret.yaml`.
- The project name stays the same, so the first apply adopts the running containers without recreating them. CI tests this (`test/integration/import.sh`).
- Warnings cover what cannot be carried over: local builds, `env_file` references, and variables that came from the shell.

Swarm stacks and Portainer follow with their own issues.

## Rules

- NepFleet commits only what it owns, such as `lock.yaml`, rendered output and imported stacks. Each commit message says why the change was made.
- Hand edits are fine. The next `plan` shows exactly what they change.
- Copying the repository and the age key to a new backend is a complete migration.
