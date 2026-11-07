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
- **A CLI install:** `nepfleet token create` must see the same state directory as the server: set `NEPFLEET_STATE` for non-interactive SSH, or use the container.
