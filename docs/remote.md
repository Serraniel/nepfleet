# Remote machines

NepFleet can look at a machine over SSH before it does anything with it:

```sh
nepfleet remote probe ssh://me@nas.local        # or an alias from ~/.ssh/config
nepfleet remote probe nas --json
```

It reports the system and architecture, your user's rights, Docker and Compose, running projects, the swarm role, and whether NepFleet or a fleet repository is already there. Then it says what fits: connect, adopt, or set up new.

- **Read-only:** a fixed script goes over SSH's stdin and nothing is installed or written. The only thing it starts is `sudo -n true`, to learn whether sudo needs a password.
- **Your SSH setup:** it uses the system `ssh`, with your keys, agent and config. NepFleet never asks for a password.
- **Host keys:** an unknown or changed host key is refused, never accepted. Connect once with `ssh` and check the fingerprint.
- **Remote output is untrusted:** only known fields are read, and control characters are removed before anything is shown.

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

## Connecting

`nepfleet remote connect` keeps an SSH tunnel to the server's API on the remote's loopback, reconnecting after failures. No port opens on either side: the local end is a private socket.

- **Access comes from SSH:** over SSH, the daemon runs `nepfleet token create` on the remote (in its container if there is one) for an `apply`-scope token named `control-<this machine>`.
- **Tokens stay in memory:** they live for 24 hours, are renewed every 12, and are revoked on disconnect, so nothing on your disk grants access. If the remote can't be reached to revoke one, it expires.
- **Clients hold no tokens:** requests to `/v1/remotes/<name>/api/...` on the daemon's socket go to the remote's `/api/...` with the token added.
- **What the remote needs:** a running NepFleet server on port 7480, and SSH allowing TCP forwarding (`AllowTcpForwarding`).
- **If the daemon is killed outright** (`kill -9`), its `ssh` process stays until the connection drops, and the next start replaces the socket. The token it held expires within a day.
- **A CLI install:** `nepfleet token create` must see the same state directory as the server: set `NEPFLEET_STATE` for non-interactive SSH, or use the container.

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

Any web page you open can send requests to a local port, and this one reaches all your fleets, so:

- **Token:** every request needs the token in `<control dir>/http.token`, which a web page can't read.
- **Origins:** requests from a browser are refused unless they come from an exact allowed origin.
- **Host:** the `Host` header must be the loopback address itself, which stops DNS rebinding.
- **Loopback only:** other addresses are refused. To reach it from elsewhere, use SSH.

Each connected remote then works as a backend at `http://127.0.0.1:7481/v1/remotes/<name>`.

## Platforms

The client side (`nepfleet remote`, `nepfleet control`) runs on Linux, macOS and Windows, using the system's `ssh` (Windows ships OpenSSH). Servers and the hosts NepFleet manages run Linux.

On Windows, a tunnel's local end is a loopback port instead of a private socket, because its `ssh` can't forward to a Unix socket. Other local users could reach that port, but the remote's API still asks for the token, which only the daemon holds.
