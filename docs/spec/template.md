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

To start from a compose file you already have:

```sh
nepfleet template generate compose.yaml --name myapp --out templates/
```

It writes `myapp/template.yaml` and `myapp/compose.yaml`, then lists what it guessed and what needs you. Treat the result as a draft: nothing has run it.

- **Variables** become inputs, typed by their name and default.
- **Secrets** are never carried over. Values under names like `*_PASSWORD` become generated secrets, and names like `*_TOKEN` or `API_KEY` become secret inputs. Names that don't look secret stay as they are, so read through the rest.
- **The web UI** is a guess at the likeliest service and port. That port moves to Caddy labels and `spec.web`; other published ports stay and are listed.
- **Volumes** are marked for backup, and names are changed to fit the template's rules.
- **Refused:** services that build their own image, and lowercase variables.
- **Listed for you:** missing tags (`template validate` refuses those), the Docker socket, host paths and privileged services.

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
    metrics: false             # nepfleet.metrics.* labels (Prometheus)
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

An empty value for an `enum`, `bool`, `int`, `hostname`, `url`, `email` or `gpu` input means unset: the default applies, or a required input is reported as missing. For `string`, `text`, `path`, `device` and `secret`, an empty value clears an optional default.

## Web UI

A template with a web UI names its service and port, so a stack can also be published on a LAN port instead of through Caddy (see access modes in [the fleet repository](fleet-repo.md#hosts-and-access-modes)):

```yaml
spec:
  web: {service: jellyfin, port: 8096}
```

### Behind Anubis

A stack can put Anubis in front of the web UI (`botCheck`, see [the fleet repository](fleet-repo.md#checking-visitors-anubis)). Visitors in a browser then solve a check, which the app's own clients can't. List the paths those use, so they skip it:

```yaml
spec:
  web: {service: synapse, port: 8008}
  botCheck:
    allow: ["^/_matrix/", "^/_synapse/client/", "^/\\.well-known/matrix/"]
```

- **What belongs here:** paths for mobile and desktop apps, APIs, federation, WebDAV, git over HTTPS, and webhooks from other services.
- **`service` and `port`** name what Anubis goes in front of, for a template without `spec.web`: an app that mustn't be offered on a LAN port, because it needs its HTTPS domain (Mastodon, Vaultwarden). By default it's `spec.web`.
- **`supported: false`** refuses the check, for apps it would break however the paths are set, such as an API without a web page.
- Paths are Go regular expressions, matched against the request path.

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

When an input has a value, NepFleet adds it to the rendered compose file: a `device` input as `devices: [<host path>:<target>]` (target defaults to the host path), `intel`/`amd` as `/dev/dri`, `nvidia` as a GPU reservation (needs the NVIDIA container toolkit). `none` (or an empty device) adds nothing. `nepfleet new` lists the host's USB serial devices by their stable `/dev/serial/by-id` path and its GPUs; `doctor` reports configured devices and GPUs the host lacks. Device paths may contain letters, digits and `._+@/-` (no `:`, which compose's device syntax splits on). For intel/amd, the host's render group is added by number (`group_add`), so images running as a non-root user can use `/dev/dri`. Devices inherited through a merge key (`<<: *base`) are kept. Swarm cannot map host devices or GPUs: a swarm stack with hardware chosen is refused. A device is bound when the container is created: after a stick is replugged and gets a new device number, re-apply (or restart) the stack so the container sees it again. An NVIDIA GPU is added next to existing `deploy.resources` limits.

## Variables

Inside the compose file and `files/`, you can use the following variables:

- Every `inputs` and `generate` name.
- The builtins:
  - `NEPFLEET_STACK`: the stack name.
  - `NEPFLEET_INGRESS_NETWORK`: the external network Caddy listens on.
  - `TZ`.
  - `NEPFLEET_URL`: where users reach the stack's web UI. It's `https://<DOMAIN>`, or `http://<host>:<port>` for a stack in access mode port. Use it for links (Homepage, Kuma) and for app settings such as a base URL.
  - `NEPFLEET_AGE_RECIPIENTS`: the fleet's age public keys, comma separated. Encrypt backups to these, and the fleet key can read them.
  - `NEPFLEET_NO_BACKUP_VOLUMES`: a regular expression alternation of the Docker volume names (`<stack>_<volume>`) that templates of stacks on the same host mark `backup: false`, such as caches and models. It's `$^` (matches nothing) when there are none, so `/backup/volumes/(${NEPFLEET_NO_BACKUP_VOLUMES})/` is a safe exclude. It's only computed for templates that use it.

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

Write labels on the service as usual. In swarm mode, NepFleet moves them to `deploy.labels`, where Caddy, AutoKuma, Homepage and Prometheus look for them.

### Metrics

With `supports.metrics: true`, the services that expose Prometheus metrics say where to find them:

```yaml
labels:
  nepfleet.metrics.port: "9090"      # required: a port, or one ${VAR}
  nepfleet.metrics.path: /metrics    # optional, default /metrics
  nepfleet.metrics.scheme: http      # optional: http (default) or https
```

The system Prometheus stack discovers these services through the Docker socket proxy (`docker_sd_configs`, or `dockerswarm_sd_configs` with `role: tasks` on swarm). It keeps only services with `nepfleet.metrics.port` and labels their metrics with the stack and service, taken from the Compose or Stack labels. A service with metrics must be on the ingress network (`name: ${NEPFLEET_INGRESS_NETWORK}`), which validation checks: Prometheus scrapes there, so each container has exactly one address, the same network Caddy uses. Exporters and workers without a web UI join it for that reason too. Validation enforces the same rules as for the other integrations: the flag needs the labels, and the labels need the flag. A service with any `nepfleet.metrics.*` label also needs the port, and unknown keys are refused.

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
        run: [sh, -c, "pg_dump -U postgres -d app > /var/lib/postgresql/data/dump.sql"]
      - name: switch images
        deploy: true            # apply the new compose file
        wait: healthy
      - name: restore
        service: db
        run: [sh, -c, "psql -U postgres -d app -v ON_ERROR_STOP=1 -f /var/lib/postgresql/data/dump.sql"]
```

A new Postgres can't start on an older major version's files, so this needs `PGDATA` to name a folder per version in the volume (`PGDATA: /var/lib/postgresql/data/17`): the new version starts empty beside the old one's data, and the dump is in a place both see. `internal/deploy/migrate_pg_test.go` runs exactly this on real Postgres 16 and 17, including a failed restore going back. `ON_ERROR_STOP` matters: without it, `psql` reports success after a failed statement, so nothing would roll back.

Steps run only inside the stack's own containers. A failed step triggers `rollback`. Your own fleet repository can define migrations for your own templates.

How an upgrade runs:

- **Where it starts:** each apply records the template version in the stack's manifest. A stack last applied before versions were recorded has no starting point: that apply runs no migration and records the version, and if the template has migrations, it asks you to confirm that none is needed.
- **When one is needed:** every migration whose `from` matches on the way to the new version has to run. An upgrade is refused if a migration lies on the way that the stack's version can't reach, or if it goes to another major version (or another minor below 1.0) with no migrations leading to it. Other upgrades just apply. Going to an older version runs no steps and asks for confirmation, as the data stays as the newer version left it; a rollback works this way, with `nepfleet rollback --confirm` where nothing can ask. Upgrading again runs the migrations again, so restore a backup when going back past one.
- **Order:** the data is snapshotted, the old version starts again and passes its health checks, the steps before `deploy: true` run in it, the new version starts and passes its health gate, and the remaining steps run in it. Without a deploy step, the new version starts last.
- **Confirmation:** the plan lists the steps, and it counts as destructive: you confirm by typing the stack's name, or with `--confirm`.
- **On failure:** `rollback: restore` restores the snapshot and the previous version. `rollback: revert` starts the previous version and keeps the data as it is. Volumes kept out of snapshots (`snapshot: false`, `snapshotExclude`) aren't restored, so a migration shouldn't change them.
- **Interrupted:** if NepFleet is killed mid-migration, nothing finishes the rollback, and the next apply sees the old version and runs the migration again. Look at the stack before applying.
- **Refused:** `backup: required` or `rollback: restore` with snapshots off or nothing to snapshot (the stack is down, or all its volumes are excluded), swarm stacks, and paths of several migrations. Those need each version's own compose file, so apply the first version named in the error, then the next.
