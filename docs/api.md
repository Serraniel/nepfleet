# HTTP API

`nepfleet serve` answers the UI, scripts and widgets. It listens on `127.0.0.1:7480` by default, and refuses other addresses without [TLS](#tls).

```sh
nepfleet serve --fleet ~/fleet --state /var/lib/nepfleet --allowed-origins https://ui.example.org
```

Requests carry an [API token](security.md#api-tokens): `Authorization: Bearer nft_…`.

| Endpoint | Scope | Answer |
|---|---|---|
| `GET /healthz` | none | `204` |
| `GET /api/v1/info` | none | name, version, API version |
| `GET /api/v1/stacks` | read | the stacks the token covers |
| `GET /api/v1/stacks/{name}/plan` | read | what `apply` would change (the `nepfleet plan --json` format) and its `id`; secrets are masked |
| `POST /api/v1/stacks/{name}/apply` | apply | starts applying the reviewed plan: `{"plan": "<id>", "confirmDestructive": false}` → `202` with a job |
| `GET /api/v1/jobs/{id}` | read | a job's state (`running`, `succeeded`, `failed`), error and log |
| `GET /api/v1/stacks/{name}/history?n=20` | read | the last `n` commits (at most 200) that touched the stack, newest first; `applied` marks the revision running now |

- **Stacks a token doesn't cover:** a token limited to stacks gets `404` for the others, the same as for a stack that doesn't exist.
- **Fresh reads:** every request reads the fleet repository again, so edits and pulls show up without a restart.
- **One operation at a time:** plans fetch template sources and render, so they run one after another.
- **Errors:** a stack that can't be read gets `500` with a generic message. The details (paths, git output) only go to the server's log.

## TLS

Tokens must not cross a network in the clear, so a plain-HTTP address beyond loopback is refused. Choose one:

| Flags | For |
|---|---|
| `--tls-cert FILE --tls-key FILE` | Your own certificate, e.g. from your ACME client. Renewed files are picked up within a minute, without a restart. If a renewed pair can't be read, the last good one stays. |
| `--tls-domain api.example.org` | A certificate from Let's Encrypt, kept in `<state>/api/acme`. It uses the TLS-ALPN challenge, so the domain must reach this server on port 443 (`--listen :443`). Using it accepts Let's Encrypt's terms. |
| `--behind-proxy` | A reverse proxy in front terminates TLS, such as the bundled Caddy. The Docker image uses this by default. Add `--trusted-proxies` so the lockout counts clients, not the proxy. |

With TLS on, the server allows TLS 1.2 and newer and sends HSTS.

Applying and rolling back over the API come next ([#38](https://github.com/Serraniel/nepfleet/issues/38)).
## Applying

1. **Get the plan** and show it to the person: `GET …/plan`.
2. **Send back its `id`:** `POST …/apply` with `{"plan": "<id>"}`.
   - The server plans again and applies only if nothing changed. Otherwise it answers `409`, and you show the new plan.
   - A plan with destructive changes also needs `"confirmDestructive": true`.
3. **Poll the job:** `GET /api/v1/jobs/{id}` (the `Location` header) until it is no longer `running`. The log is what `nepfleet apply` prints.

- **One operation at a time:** a second apply while one runs gets `409`, and a plan gets `503` with `Retry-After`.
- **Plan IDs:** they change when the server restarts, so plan again after a restart.
- **Reviewed means applied:** the job checks the ID again on the plan it applies. If the repository changed after the `202`, the job fails with "the plan changed" and nothing is applied.
- **One apply per stack on a host:** the CLI and the API share a lock in `<state>/locks`, so a second apply of a stack is refused.
- **Stopping the server:** it waits for a running apply, and a second signal aborts it. Give it enough time, or the service manager kills it first and the stack stays half updated: `stop_grace_period: 10m` with Compose (the shipped `compose.yaml` sets it), `TimeoutStopSec=10min` with systemd.
- **Jobs:** the last 50 are kept in memory.
- **Server log:** every apply records the token's name and ID there.

Rolling back over the API comes with commits authored by the person who asks ([#37](https://github.com/Serraniel/nepfleet/issues/37)).
