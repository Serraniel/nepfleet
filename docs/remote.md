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
