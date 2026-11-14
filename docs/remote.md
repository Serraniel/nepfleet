# Remote machines

NepFleet can look at a machine over SSH before it does anything with it:

```sh
nepfleet remote probe ssh://me@nas.local        # or an alias from ~/.ssh/config
nepfleet remote probe nas --json
```

It reports the system and architecture, your user's rights, Docker and Compose, running projects, the swarm role, and whether NepFleet or a fleet repository is already there. Then it says what fits: connect, adopt, or set up new.

- **Read-only:** a fixed script goes over SSH's stdin and nothing is installed or written. The only thing it starts is `sudo -n true`, to learn whether sudo needs a password.
- **Your SSH setup:** it uses the system `ssh`, with your keys, agent and config. It never asks for an SSH password: only keys work.
- **Keys with a passphrase:** `ssh` runs without a terminal here (the daemon and the app reconnect in the background), so a key with a passphrase is used through an SSH agent.
  - **Which agent:** your session's agent if one runs (`SSH_AUTH_SOCK`); otherwise the control daemon starts its own, which only you can use, and stops it with the daemon.
  - **Unlocking:** when SSH refuses the login and one of the keys `ssh` would offer has a passphrase, NepFleet names that key. The app asks for the passphrase; on the command line, `host bootstrap`, `host install` and `host apply` ask on the terminal (without echo), and `nepfleet key unlock [~/.ssh/key …]` unlocks keys into your agent.
  - **How:** `ssh-add -t 8h <key>`, with NepFleet as `SSH_ASKPASS`. The passphrase reaches it in that one child process's environment, never as an argument, a file or a log line. Only private keys in `~/.ssh` are accepted, and the key is in the agent for 8 hours, then has to be unlocked again.
  - **Your own agent instead:** run one for your session (on Arch, `systemctl --user enable --now ssh-agent`, and `SSH_AUTH_SOCK=${XDG_RUNTIME_DIR}/ssh-agent.socket` in `~/.config/environment.d/`). Then a key unlocked once, by `ssh-add` or by NepFleet, works for both. KDE's KWallet and GNOME Keyring run an agent too.
- **Host keys:** an unknown or changed host key is refused, never accepted. Connect once with `ssh` and check the fingerprint.
- **Remote output is untrusted:** only known fields are read, and control characters are removed before anything is shown.

## Checking a host's security

```sh
nepfleet remote probe ssh://me@nas --security
nepfleet doctor --security       # on this host
```

It lists the worst first: password or root login over SSH, whether a firewall runs and that Docker's published ports bypass it, what listens beyond the machine (databases and the Docker API are flagged), automatic updates, pending reboots, privileged containers, `docker` group members, and whether anything bans repeated failed logins.

- **Compared with your fleet:** `doctor --security` in a fleet also names what Docker publishes for projects the fleet doesn't manage on this host, and who publishes each flagged database. Containers this user can't see through Docker show only as listening ports.
- **For the UI and alerts:** `--json` prints the findings. `--notify` sends problems new since the last check to the fleet's channels as `host.security_problem`, once each, so run it on a schedule. A problem that was fixed and comes back counts as new.
- **Read-only:** a fixed script with no `sudo`, so it changes nothing. `--notify` only keeps what it reported, in `<state>/security`.
- **Honest about limits:** what this user can't read shows as `?`, never as fine. It looks at the first matching SSH setting in the config files, not at what `sshd` would resolve, and "listening" doesn't say whether your router or provider lets traffic through.
- **Fixing:** it only reports; applying changes comes with `nepfleet host bootstrap` (#15).

## Preparing a machine

```sh
nepfleet host bootstrap ssh://me@server --fleet ~/fleet
```

It probes the machine and shows a plan, with every step that runs as root marked, then asks. `--yes` skips the question. What it can do:

- Install Docker and the Compose plugin from the distribution's own packages (Arch, Debian, Ubuntu, Raspberry Pi OS, Fedora). On other systems it says so, and you install them yourself.
- Start Docker, now and at boot.
- Add your user to the `docker` group, which is effectively root on that host.
- Create the fleet's ingress network, and add the machine to `hosts/`.

- **Root:** it needs root or a user who may use `sudo`. If sudo asks for a password, NepFleet asks you for it once per run: on the terminal without echo, like sudo (or from the first line of stdin with `--sudo-password-stdin`, for scripts), and in the app's setup dialog. The password goes to `sudo -S` on the machine, over SSH's encrypted stdin only: never on a command line, in a file or a log, and it is never stored. A user who may not use sudo learns so when the password is tried (sudo may report that attempt to the admin), and hosts with `Defaults requiretty` need that rule removed or a root login. `--sudo-password-stdin` goes with `--yes`, as there is then no terminal to confirm on.
- **Failures:** it stops at the first failed step. Steps can be run again safely.
- **Arch:** no `pacman -Sy`, as partial upgrades break Arch. If the package database is too old, update the system first.

The SSH, firewall and update fixes from the security check come later.

## Installing NepFleet on it

Once Docker runs there, install NepFleet as a container, as [Install](install.md#container) describes, without typing it by hand:

```sh
nepfleet host install ssh://me@server --name server
```

It probes the machine and shows a plan, then asks:

1. Create `/srv/nepfleet`, owned by your SSH user. This needs root or `sudo`, which may ask for its password as above. If you created it yourself, pass `--skip-dir`.
2. Write `compose.yaml`, the one this NepFleet ships, pinned to its own version, and `.env`. The API listens on the machine's loopback only.
3. Pull the image.
4. Generate the fleet's age key on the machine. It never leaves it: back it up yourself, as the command says at the end.
5. Create the fleet repository, encrypting to that key.
6. Start the API and wait until it's healthy.

- **Refused on purpose:** a machine where NepFleet runs or a fleet repository exists (connect or adopt instead), one without Docker (run `host bootstrap` first), and one that isn't Linux on x86_64 or aarch64.
- **Running it again** is safe: a key or fleet that's there stays.
- **Then connect:** `nepfleet remote add` and `nepfleet remote connect`, or the Fleets page of the app.

## The control daemon

`nepfleet control` is a small service on your own machine that remembers your remotes and will own the connections to them. Clients talk to it, so closing one changes nothing. `nepfleet remote` starts it when it isn't running, and it keeps running afterwards.

```sh
nepfleet remote add ssh://me@nas.local --name nas   # probes it first; an unreachable machine isn't added
nepfleet remote list
nepfleet remote refresh nas
nepfleet remote connect nas      # keep a tunnel to its API; survives restarts of the daemon
nepfleet remote disconnect nas
nepfleet remote remove nas
nepfleet control status
```

- **Access:** a Unix socket that only you can open, in a private directory (`~/.local/state/nepfleet/control`). It refuses a directory that others can enter.
- **Checks:** it only probes when you add or refresh a remote, because each probe shows up in the remote's auth log.
- **To start it at login:** run `nepfleet control` from a systemd user unit.
- **Setting a remote up from the app:** the daemon offers what `host bootstrap` and `host install` do, for clients such as the app's wizard:
  - `GET /v1/remotes/<name>/setup?kind=bootstrap|install` probes the machine again and returns the plan, with an `id`, or an `error` saying why it can't run, and `sudoPassword: true` when its root steps need the sudo password.
  - `POST /v1/remotes/<name>/setup` with `{"kind", "plan": "<id>", "sudoPassword"}` starts it only if the plan is still the one reviewed (otherwise `409`). `sudoPassword` is needed when the plan says so, is used for that run only, and appears in no job, log or error.
  - `GET /v1/remotes/<name>/setup/job` follows it: state and log.
  - A setup goes on when the window closes, and one runs per remote at a time.
- **Network mounts from the app:** `kind=mounts` does what `nepfleet host apply <name>` does: it makes the machine's NFS, SMB and SSHFS mounts match `hosts/<name>.yaml`, and they stay after a reboot.
  - It plans from a local clone of the fleet repository, chosen once with `PUT /v1/settings {"fleetDir": "/path/to/fleet"}` (the app's Settings). Share passwords are decrypted from `fleet.secret.yaml` with this computer's age key, as on the command line.
  - The passwords go only to the machine, over SSH. They are in no plan, job, log or answer, and the NepFleet server never sees them.

## Connecting

`nepfleet remote connect` keeps an SSH tunnel to the server's API on the remote's loopback, reconnecting after failures. No port opens on either side: the local end is a private socket.

- **Access comes from SSH:** over SSH, the daemon runs `nepfleet token create` on the remote (in its container if there is one) for an `apply`-scope token named `control-<this machine>`.
- **Tokens stay in memory:** they live for 24 hours, are renewed every 12, and are revoked on disconnect, so nothing on your disk grants access. If the remote can't be reached to revoke one, it expires.
- **Clients hold no tokens:** requests to `/v1/remotes/<name>/api/...` on the daemon's socket go to the remote's `/api/...` with the token added.
- **What the remote needs:** a running NepFleet server on port 7480, and SSH allowing TCP forwarding (`AllowTcpForwarding`).
- **If the daemon is killed outright** (`kill -9`), its `ssh` process stays until the connection drops, and the next start replaces the socket. The token it held expires within a day.
- **A CLI install:** `nepfleet token create` must see the same state directory as the server: set `NEPFLEET_STATE` for non-interactive SSH, or use the container.

## Events

The daemon follows the [event feed](notifications.md#event-feed) of every connected fleet and merges them, each event labelled with its fleet. It remembers how far it read, so it catches up after a dropped tunnel or a restart. As it keeps running when clients close, nothing is lost while an app is closed, as long as the remote still holds the event (the last 1000).

- `GET /v1/events?after=ID` returns events with the unread count; `POST /v1/events/seen` with `{"id": N}` marks them read.
- **A newly connected fleet starts from now:** its history isn't news.

## Terminal client

`nepfleet tui` shows your machines and, for a connected one, its stacks, each stack's plan with diffs, and applying it with a live log. It starts the daemon if needed, and closing it changes nothing: a running apply goes on.

- **Applying:** it asks first. A plan with destructive changes needs the stack's name typed.
- **Remote text:** control characters are removed before anything is shown, so a remote machine can't drive your terminal.

## Browsers

The web UI can't open a Unix socket, so the daemon can also listen on a loopback port:

```sh
nepfleet control --http 127.0.0.1:7481 --allowed-origins http://localhost:5173
nepfleet control token     # paste this into the UI once
```

`nepfleet control http 127.0.0.1:7481 --allowed-origins …` turns the port on in a running daemon without touching its tunnels. Either way it is remembered, and origins add up. Only the socket can change these rules, so a page holding the token can't add its own origin.

Any web page you open can send requests to a local port, and this one reaches all your fleets, so:

- **Token:** every request needs the token in `<control dir>/http.token`, which a web page can't read.
- **Origins:** requests from a browser are refused unless they come from an exact allowed origin.
- **Host:** the `Host` header must be the loopback address itself, which stops DNS rebinding.
- **Loopback only:** other addresses are refused. To reach it from elsewhere, use SSH.

Each connected remote then works as a backend at `http://127.0.0.1:7481/v1/remotes/<name>`.

## Platforms

The client side (`nepfleet remote`, `nepfleet control`) runs on Linux, macOS and Windows, using the system's `ssh` (Windows ships OpenSSH). Servers and the hosts NepFleet manages run Linux.

On Windows, a tunnel's local end is a loopback port instead of a private socket, because its `ssh` can't forward to a Unix socket. Other local users could reach that port, but the remote's API still asks for the token, which only the daemon holds.
