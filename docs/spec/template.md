# Template format (`nepfleet/v1alpha1`)

A template is a directory:

```
vaultwarden/
├── template.yaml   # this spec
├── compose.yaml    # a normal compose file using ${VARIABLES}
└── files/…         # optional extra files (configs), rendered with the same variables
```

`nepfleet template validate <dir>` checks everything below. Unknown fields are errors. Every compose image must have a pinned tag (never `:latest`) and an entry in `spec.images`.

For autocompletion and validation in your editor, start `template.yaml` with:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/Serraniel/nepfleet/main/schemas/template.schema.json
```

The schema checks the structure. `template validate` also checks the compose file, the image policies and the integration labels, which a schema cannot express.

`nepfleet template index <root>` prints `index.json`, the catalogue of every template under `root`: metadata, version, images and tracks, integration flags, inputs, volumes and the available migrations. Frontends use it to list and filter templates and to build the install wizard without reading every `template.yaml`. It refuses to index an invalid template.

```yaml
apiVersion: nepfleet/v1alpha1
kind: Template
metadata:
  name: vaultwarden            # = directory name
  title: Vaultwarden
  description: Bitwarden-compatible password manager.
  tier: app                    # system | app
  category: security
  homepage: https://github.com/dani-garcia/vaultwarden
spec:
  version: 1.0.0               # template version (semver), not the app's
  compose: compose.yaml
  images:                      # update policy per image; the tag itself lives only in compose.yaml
    - name: vaultwarden/server
      track: minor             # widest auto-update: patch | minor | major
  supports:                    # integrations this template wires up
    swarm: true                # compose file also works with `docker stack deploy`
    arm64: true
    ingress: true              # caddy.* labels
    monitoring: true           # kuma.* labels (AutoKuma)
    statusPage: false          # ships a stack-local status page
    dashboard: true            # homepage.* labels
    logs: true                 # Dozzle-friendly (logs to stdout)
    autoUpdate: true
    backup: true
    sso: false                 # OIDC wiring
  inputs:                      # the wizard
    - name: DOMAIN
      type: hostname           # string|text|secret|hostname|url|email|int|bool|enum|path
      title: Domain
      required: true
  generate:                    # created once at install, stored encrypted
    - name: ADMIN_TOKEN
      kind: secret
      length: 48
      encoding: base64url      # base64url | hex | alnum
  volumes:
    - name: data
      backup: true
      shared: false            # true: must follow the service across swarm nodes
  migrations: []               # see below
```

## Hardware

Devices and GPUs are inputs like any other, so the wizard can offer what the host has and a template still works without them:

```yaml
spec:
  inputs:
    - {name: ZIGBEE_DEVICE, type: device, title: Zigbee/Thread stick}   # e.g. /dev/serial/by-id/usb-…
    - {name: GPU, type: gpu, title: Hardware transcoding, default: none} # none | intel | amd | nvidia
  hardware:
    - {input: ZIGBEE_DEVICE, services: [homeassistant], target: /dev/ttyUSB0}
    - {input: GPU, services: [jellyfin]}
```

When an input has a value, NepFleet adds it to the rendered compose file: a `device` input as `devices: [<host path>:<target>]` (target defaults to the host path), `intel`/`amd` as `/dev/dri`, `nvidia` as a GPU reservation (needs the NVIDIA container toolkit). `none` or an empty value adds nothing. `nepfleet new` lists the host's USB serial devices by their stable `/dev/serial/by-id` path and its GPUs; `doctor` reports configured devices and GPUs the host lacks. Device paths may contain letters, digits and `._+@/-` (no `:`, which compose's device syntax splits on). For intel/amd, the host's render group is added by number (`group_add`), so images running as a non-root user can use `/dev/dri`. Devices inherited through a merge key (`<<: *base`) are kept. Swarm cannot map host devices or GPUs: a swarm stack with hardware chosen is refused. A device is bound when the container is created: after a stick is replugged and gets a new device number, re-apply (or restart) the stack so the container sees it again. An NVIDIA GPU is added next to existing `deploy.resources` limits.

## Variables

Inside the compose file and `files/`, you can use the following variables:

- Every `inputs` and `generate` name.
- The builtins: `NEPFLEET_STACK` (the stack name), `NEPFLEET_INGRESS_NETWORK` (the external network Caddy listens on) and `TZ`.

Using any other `${VAR}` is a validation error, so a typo never silently becomes an empty string. Write `$${VAR}` to get a literal `${VAR}`.

Values are written to the env file single-quoted, so a `$` inside a value (Argon2 or bcrypt hashes, for example) stays literal.

## Integration labels

When the relevant `supports` flag is set, use these labels:

```yaml
labels:
  caddy: ${DOMAIN}
  caddy.reverse_proxy: "{{upstreams 80}}"
  kuma.vaultwarden.http.name: Vaultwarden      # Compose does not interpolate label keys
  kuma.vaultwarden.http.url: https://${DOMAIN}/alive
  homepage.group: Security
  homepage.name: Vaultwarden
  homepage.href: https://${DOMAIN}
```

Write labels on the service as usual. In swarm mode, NepFleet moves them to `deploy.labels`, where Caddy, AutoKuma and Homepage look for them.

## Migrations

A migration moves an installed stack from one template version to the next. NepFleet chains migrations to find an upgrade path, and it refuses to upgrade when no path exists.

```yaml
migrations:
  - from: ">=1.0.0 <2.0.0"
    to: 2.0.0
    description: Postgres 16 → 17 via dump and restore
    backup: required            # required | recommended | none
    rollback: restore           # restore = restore backup + previous commit; revert = previous commit only
    steps:
      - name: dump
        service: db
        run: [sh, -c, "pg_dumpall -U postgres > /backup/dump.sql"]
      - name: switch images
        deploy: true            # apply the new compose file
        wait: healthy
      - name: restore
        service: db
        run: [sh, -c, "psql -U postgres < /backup/dump.sql"]
```

Steps run only inside the stack's own containers. A failed step triggers `rollback`. Your own fleet repository can define migrations for your own templates.
