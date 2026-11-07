# HTTP API

`nepfleet serve` answers the UI, scripts and widgets. It listens on `127.0.0.1:7480` by default; put a TLS proxy in front before you expose it (see [security](security.md)).

```sh
nepfleet serve --fleet ~/fleet --state /var/lib/nepfleet --allowed-origins https://ui.example.org
```

Requests carry an [API token](security.md#api-tokens): `Authorization: Bearer nft_…`.

| Endpoint | Scope | Answer |
|---|---|---|
| `GET /healthz` | none | `204` |
| `GET /api/v1/info` | none | name, version, API version |
| `GET /api/v1/stacks` | read | the stacks the token covers |
| `GET /api/v1/stacks/{name}/plan` | read | what `apply` would change (the `nepfleet plan --json` format); secrets are masked |
| `GET /api/v1/stacks/{name}/history?n=20` | read | the last `n` commits (at most 200) that touched the stack, newest first; `applied` marks the revision running now |

- **Stacks a token doesn't cover:** a token limited to stacks gets `404` for the others, the same as for a stack that doesn't exist.
- **Fresh reads:** every request reads the fleet repository again, so edits and pulls show up without a restart.
- **One operation at a time:** plans fetch template sources and render, so they run one after another.
- **Errors:** a stack that can't be read gets `500` with a generic message. The details (paths, git output) only go to the server's log.

Applying and rolling back over the API come next ([#38](https://github.com/Serraniel/nepfleet/issues/38)).
