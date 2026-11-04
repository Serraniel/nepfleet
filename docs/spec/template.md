# Template format (`nepfleet/v1alpha1`)

A template is a directory:

```
vaultwarden/
├── template.yaml   # this spec
├── compose.yaml    # a normal compose file using ${VARIABLES}
└── files/…         # optional extra files (configs), rendered with the same variables
```

`nepfleet template validate <dir>` checks everything below. Unknown fields are errors. Every compose image must have a pinned tag (never `:latest`) and an entry in `spec.images`.

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
