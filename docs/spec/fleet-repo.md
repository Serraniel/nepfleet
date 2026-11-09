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

`schemas/` in the nepfleet repository holds JSON Schemas for [`fleet.yaml`](../../schemas/fleet.schema.json), [`stack.yaml`](../../schemas/stack.schema.json) and [`template.yaml`](../../schemas/template.schema.json). They are generated from the Go types that parse these files, so they cannot drift (`go run ./tools/schema`; CI fails when they are stale), and each release attaches them. `nepfleet init`, `new` and `import` write a `# yaml-language-server: $schema=…` line, so editors with the YAML language server (VS Code, Neovim, Helix, Zed) complete and check fields as you type. `hosts/*.yaml` has [its own](../../schemas/host.schema.json).

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

## Hosts and access modes

`hosts/<name>.yaml` describes a host. Without a file, a host's address is its name, and it uses ports 8000–8999:

```yaml
apiVersion: nepfleet/v1alpha1
kind: Host
metadata:
  name: nas
spec:
  address: nas.local            # how devices on the LAN reach it (a name or an IPv4 address)
  ports: {from: 8000, to: 8999} # LAN ports NepFleet hands out to stacks
```

A stack's web UI is reached through Caddy on its domain (the default), or on a port of the host on the LAN:

```yaml
spec:
  access: {mode: port, port: 8060}   # mode: domain (default) | port
```

- **Port mode:**
  - The template's web service is published on that port, and its Caddy labels are dropped, so it has no route through the ingress.
  - Templates that ask for a `DOMAIN` get the host's address there.
  - `${NEPFLEET_URL}` is `http://<address>:<port>`.
- **Domain mode:** `${NEPFLEET_URL}` is `https://<DOMAIN>`.
- **Creating a stack:** `nepfleet new <template> --access port` picks the lowest port in the host's range that no stack on it uses, or takes `--port N`. It doesn't ask for a domain.
- **Conflicts:** two stacks on one host with the same port make `plan` and `apply` fail.
- **Which templates:** only templates that declare their web UI (`spec.web`) can use port mode, and not when that service uses the host's network.
- **No ingress at all:** in port mode, the Caddy labels of every service of the stack are dropped.
- **Swarm:** port mode is for compose stacks for now.

## Config files

Files under `stacks/<name>/files/` are versioned with the stack and rendered into its working directory: `files/config/app.yaml` becomes `config/app.yaml`, which the compose file mounts as `./config/app.yaml`.

- **Variables:** `${VAR}` is replaced as in template files, including secrets. Write `$${VAR}` for a literal `${VAR}`.
- **Overriding a template:** a file with the same destination as a template's file replaces it; the template's other files stay. So the template ships defaults, and the repository owns what it overrides.
- **Files the app edits:** a template file marked `keep: true` (settings an app saves from its UI) is written when it's missing, and otherwise left as the app has it: plans show no change for it, and apply writes it back unchanged. Delete it from the working directory to get the template's version again. A copy in `files/` still overrides it, as the repository then owns it.
- **Changes:** plans show these files like compose changes, with secret values masked. `history` and `rollback` cover them, as they are part of the stack's directory, and deleted files are removed on apply.
- **Restarts:** files change in place, so an app that only reads its config at start needs a restart to see it.
- **Secrets on the host:** files stay readable for the container's user, and the stack's working directory is `0700` to keep other users of the host out.
- **Limits:** this is for config, not data: 1 MiB per file, 8 MiB and 500 files per stack. Symbolic links and the names `compose.yaml`, `.env` and `.nepfleet/` are refused. Keep what apps write themselves in volumes.

## Network shares on a host

Hosts mount network shares from `hosts/<name>.yaml`, so they're versioned rather than hidden in `fstab`:

```yaml
spec:
  mounts:
    - {name: media, type: nfs, source: "nas.local:/media", target: /mnt/media, options: [ro]}
    - {name: box, type: cifs, source: "//u1.your-storagebox.de/backup", target: /mnt/box, user: u1, passwordSecret: BOX_PASSWORD}
    - {name: far, type: sshfs, source: "me@far.example:/data", target: /mnt/far}
```

`nepfleet host apply <name> [ssh://address] [--fleet DIR]` shows a plan and asks, then installs the tools and writes systemd units that mount each share on first use.

- **Defaults:** `_netdev` and `nofail`, so an absent share can't stop a boot. NFS uses 4.2. SMB uses 3.1.1 with encryption.
- **Passwords:** `passwordSecret` names a value in `fleet.secret.yaml`. On the host it is only in a root-only file, never in the unit, which anyone may read.
- **SSHFS:** logs in with a key you put at `/etc/nepfleet/mounts/<name>.key`, as root. It is your host key to give, so NepFleet doesn't create it. The remote's host key must be known to root.
- **Stacks wait for their share:** an apply refuses a stack that bind-mounts a path on a share that isn't mounted, so a container never writes into the empty folder underneath. This check needs NepFleet running natively: in a container, it can't see the host's mounts and is skipped.
- **Removing:** a share taken out of the file gets unmounted and its units removed. Data stays.
- **What not to put on one:** databases and anything locking files heavily, as network shares can lose or delay locks.
- **Not for:** volumes that follow a service across swarm nodes. Use [network storage](#network-storage) for those.

## Network storage

Stack volumes can live on a NAS share or a Storage Box. Name the storage once in `fleet.yaml`:

```yaml
spec:
  storage:
    nas:
      type: cifs                    # cifs (SMB) | nfs
      address: 192.168.1.10
      share: media                  # cifs: the share name; nfs: the exported path (/exports/media)
      username: jellyfin            # cifs
      passwordSecret: NAS_PASSWORD  # key in fleet.secret.yaml (nepfleet secret set --global NAS_PASSWORD)
      options: [vers=3.1.1, uid=1000, gid=1000]
```

Then point a stack's volume at it in `stack.yaml`:

```yaml
spec:
  volumes:
    media: {storage: nas, path: /movies}   # a folder in the share; empty = the share itself
```

- **How it's mounted:** the volume becomes a Docker `local` volume that mounts the share (`driver_opts` type, device, options). Docker mounts it when a container starts, on whichever node runs it, so it works in swarm too.
- **Password:** it's written only to the stack's env file, as `NEPFLEET_STORAGE_<NAME>_PASSWORD`, and plans show it masked. Mount options are comma separated, so a password can't contain a comma.
- **Volume name:** the volume gets its own name, `<stack>_<volume>_<storage>`. A local volume with the same key may already exist, and Docker never changes an existing volume's options. Data in an old local volume is not moved.
- **Only volumes:** a stack can only put volumes it declares on network storage. A host path (bind mount) stays a host path. For mounts on the host itself, see #110.

## Shared folders

Name host folders that several stacks use once, in `fleet.yaml`:

```yaml
spec:
  paths:
    media: /mnt/media                      # Jellyfin, Navidrome and Tdarr read it
    labels: /mnt/data/docker/homebox/labels  # Homebox writes labels, Home Assistant prints them
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

### Automatic updates

`nepfleet updates apply` is for a timer. Each run, per stack:

- **`updates: auto`:** a tag rebuilt upstream, such as a security rebuild of `:1`, is applied with the usual health gate and rollback, and the new digest is committed to `lock.yaml`. The commit's author is `automatic updates`.
- **`updates: notify`:** sends `update.available`, once per change.
- **`updates: off`:** nothing.

```yaml
# fleet.yaml
spec:
  updates:
    window: { from: "03:00", to: "05:00", timezone: Europe/Berlin }   # when auto may act; default any time
```

- **A newer tag isn't automatic,** even within its track: it comes with a template update, so `auto` only notifies about it.
- **It waits rather than guesses:** destructive changes and migrations are refused and reported, and a stack with uncommitted changes is left alone until they're committed. Outside the window it waits for the next run, unless you pass `--now`.
- **Pushing is yours:** it commits locally, so a remote fleet repository needs its own push.

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
- **The template's commit** is pinned too: `template: {source: official, commit: <sha>}`.
  - A stack renders from that commit, even after the source's branch has moved, so a stack's revision means the same files later.
  - `nepfleet apply --update-lock` moves it to the source's current commit, and the plan shows `template official@<old> → official@<new>`. If the files don't change, only the pin moves.
  - The pinned commit is checked out once into the source cache (`sources/pinned/<source>@<commit>`).
  - Raising the template `version` in `stack.yaml` moves the pin as well, and the plan shows the move.
  - `--no-lock` writes neither image digests nor the template pin.
  - Local path sources aren't pinned.

## Copies of the key and the repository

Every secret is encrypted to the age keys in `fleet.yaml`. If you lose the private key, every secret is gone, so keep a copy somewhere other than this host:

- **`nepfleet key backup --out /media/usb/nepfleet-key.age`:** writes the key encrypted with a passphrase that `age` asks for. With `--recipient age1…`, it's encrypted to another key instead, for example one in your password manager.
  - The file is created owner-only (0600) and never overwrites anything.
  - It may not end up in the fleet repository, also not through symlinks.
  - Restore it with `age -d -o ~/.config/sops/age/keys.txt -- nepfleet-key.age`, adding `-i <identity>` for a recipient copy.
- **A second recipient:** a second public key in `spec.secrets.age` also counts as a copy, once the files are re-encrypted (`sops updatekeys`), because its holder can decrypt every secret. Keep it on a different machine.
- **`spec.mirrors`:** lists git URLs of second remotes, such as another forge or a Storage Box over SSH.
  - `nepfleet mirror` fetches origin and pushes its branches and all tags to every mirror. A clone that is behind still publishes the current state.
  - It never force-pushes and never deletes. A mirror with commits of its own makes it fail rather than be overwritten.
  - Run it from a timer, or after pushing.

`nepfleet doctor` warns while it knows of no copy of the key (including when a recorded copy has been moved away), when the key comes from `SOPS_AGE_KEY` and cannot be checked, and while the repository has no mirror or this host has not mirrored yet.

## Blocked hosts (telemetry)

Some software in containers reports usage without asking, such as onnxruntime in Immich's machine learning, Scarf in many images, PostHog in Stirling-PDF and n8n, and Syncthing's reports. NepFleet resolves those hosts to `0.0.0.0` in every service, through `extra_hosts`, so the connection fails at once and nothing else is affected:

```yaml
spec:
  egress:
    blockTelemetry: true        # default; false turns the list off (fleet.yaml or stack.yaml)
    block: [ads.example.org]    # more exact host names (no wildcards)
```

- **Where it's set:** a stack's `egress` adds to the fleet's, and a stack's `blockTelemetry` wins.
- **What's on the list:** `TelemetryHosts` in `internal/fleet/egress.go`. It holds only hosts that serve telemetry alone, never update or package servers.
- **Overrides:** an `extra_hosts` entry a service sets itself for the same host is kept.
- **Not covered:** services that share another container's network (`network_mode: service:…`) can't have `extra_hosts`. They're left out, and they usually share the other container's hosts anyway.
- **When enabled:** the first apply after enabling shows the new `extra_hosts` lines in the plan and recreates the services once.

### Default-deny egress

Blocking known hosts only stops what someone already knows about. `mode: enforce` denies everything that isn't allowed:

```yaml
spec:
  egress:
    mode: enforce               # default off; a stack's mode replaces the fleet's
    allow:                      # host names, addresses or CIDR ranges; no wildcards
      - smtp.example.org
      - 203.0.113.5
```

- **What stays open:** private networks, so the stack's own services, the ingress and the LAN keep working. Inbound traffic and published ports aren't affected, and DNS still works.
- **What is allowed besides `allow`:** the fleet's and stack's lists both count, plus what the template declares in `spec.egress` (`[{host: huggingface.co, why: model downloads}]`) and the hosts of its `url` inputs.
- **How it works:** each service gets a `<service>-egress` sidecar that owns its network, ports and Caddy labels and sets the rules in that shared namespace. The service starts only once the rules are in place. If the sidecar fails, the service stays down rather than going out unfiltered.
- **Names:** a host is looked up when the sidecar starts and every 5 minutes, and its addresses are allowed. A failed lookup keeps the last addresses. CDNs can make a host's addresses change faster, and they're shared: allowing one name allows everything else on those addresses.
- **Raw sockets:** services lose Docker's default `NET_RAW`, which could get around the rules, unless they ask for it. Plain `ping` needs it on some images.
- **Refused, not left unfiltered:** host networking, `network_mode: bridge` or `container:`, `privileged`, `cap_add: NET_ADMIN`, and swarm stacks. Set `mode: off` for those stacks.
- **What it is for:** it catches telemetry and unwanted calls home. It isn't a boundary against an attacker who controls the sidecar's image or the Docker host.
- **When enabled:** the first apply recreates the stack's services, as they move to the sidecar's network.

#### Finding out what to allow

`mode: log` blocks nothing, and records where a stack's services go. Run it for a while, then turn that into a list to review:

```sh
nepfleet egress suggest vaultwarden
```

- **How it works:** the same sidecar, running `tcpdump` instead of setting rules. It records DNS answers and new connections, not their contents, in the sidecar's container log. These logs show what each service talks to, so treat them accordingly.
- **What it suggests:** names that were looked up and then connected to, under the name the app asked for. Addresses with no name seen are listed apart, as they may change. Names looked up but never connected to are only mentioned. What's already allowed is left out.
- **It shows what the app did, not what it should do,** so review it: telemetry shows up too.
- **Limits:** only what happened while it ran, and only as far back as the container's logs go, so log rotation and recreating the stack lose history. A DNS server other than Docker's is seen as an address, not its answers. Run `suggest` on the host where the stack runs.
- **Turning it on or off** recreates the stack's services, as it moves them to or from the sidecar's network.

#### What enforce refused

In `mode: enforce`, the sidecar also logs what it refuses, with the names looked up, so `suggest` names them:

```sh
nepfleet egress suggest vaultwarden   # in mode enforce: what was refused
```

- **Only refusals:** allowed connections aren't logged in enforce mode, so this is what broke, not everything the stack does.
- **Rate-limited:** at most 20 refusals a minute are logged, so an app retrying in a loop can't flood the log.
- **Optional:** it needs the kernel's NFLOG. Without it, rules are enforced as before, and nothing is logged.
- **Capabilities:** to run `tcpdump`, the sidecar now has `SETUID` and `SETGID` too, as in log mode. The app's own container is unchanged.

## Checking visitors (Anubis)

A public web UI attracts scrapers and bots. `botCheck` puts [Anubis](https://anubis.techaro.lol) in front of it, so a browser solves a small proof of work before it reaches the app, without a third party like Cloudflare:

```yaml
spec:
  botCheck:
    enabled: true
    allow: ["^/api/", "^/\\.well-known/"]   # paths that skip the check
    difficulty: 4                           # leading zeros; the default
```

- **How it works:** the template's web service (`spec.web`) loses its Caddy labels to a `<service>-anubis` service, which forwards what passes to the service. Caddy passes on the visitor's address. The policy is rendered to `anubis-policy.yaml` next to the compose file.
- **Who is checked:** whatever looks like a browser. Anubis's own rules deny known bad bots and AI scrapers and let good search engines and well-known paths through. Other clients pass, as they can't solve a check anyway.
- **What must skip it:** apps, APIs, federation (`/inbox`, `/_matrix`), WebDAV and git over HTTPS can't solve a check. Templates list the paths their app needs in their own `spec.botCheck.allow`. Add what else you need to the stack's `allow`, as Go regular expressions; both count.
- **Templates it would break** say `supported: false`, and the check is refused for them.
- **Visitors need JavaScript,** and pass for a week (Anubis's cookie). Its signing key is generated once and kept in the stack's secrets (`NEPFLEET_ANUBIS_KEY`), so a restart doesn't make everyone solve it again.
- **No lookups elsewhere:** the DNS blocklist is off, so visitors' addresses aren't sent to a third party.
- **Limits:** compose stacks in access mode `domain`, with one plain route to the web port (`caddy.reverse_proxy: "{{upstreams PORT}}"`). Templates that send paths to other ports or services are refused. Turning it on or off recreates the web service.

## Rules

- NepFleet commits only what it owns, such as `lock.yaml`, rendered output and imported stacks. Each commit message says why the change was made.
- Hand edits are fine. The next `plan` shows exactly what they change.
- Copying the repository and the age key to a new backend is a complete migration.
