# HTTP API

`nepfleet serve` answers the UI, scripts and widgets. It listens on `127.0.0.1:7480` by default, and refuses other addresses without [TLS](#tls).

```sh
nepfleet serve --fleet ~/fleet --state /var/lib/nepfleet --allowed-origins https://ui.example.org
```

Requests carry an [API token](security.md#api-tokens): `Authorization: Bearer nft_…`.

The server describes itself in [OpenAPI 3.1](../internal/api/openapi.yaml) at `GET /api/v1/openapi.yaml`. A test keeps that document's routes and fields the same as the code's.

| Endpoint | Scope | Answer |
|---|---|---|
| `GET /healthz` | none | `204` |
| `GET /api/v1/info` | none | name, version, API version |
| `GET /api/v1/openapi.yaml` | none | the OpenAPI document |
| `GET /api/v1/stacks` | read | the stacks the token covers |
| `GET /api/v1/templates` | read | per template source of the fleet: its commit and its catalogue (the `index.json` format); templates that don't validate are listed as `invalid` and not offered |
| `GET /api/v1/stacks/{name}/plan` | read | what `apply` would change (the `nepfleet plan --json` format) and its `id`; secrets are masked |
| `POST /api/v1/stacks/{name}/apply` | apply | starts applying the reviewed plan: `{"plan": "<id>", "confirmDestructive": false}` → `202` with a job |
| `POST /api/v1/stacks/{name}/rollback` | apply | starts rolling back to a commit from the stack's history: `{"commit": "<full ID>", "confirmDestructive": false}` → `202` with a job |
| `GET /api/v1/events?after=0&limit=100` | read | notification events newer than an ID, oldest first (at most 500); a token limited to stacks sees only those, and no host-wide events |
| `GET /metrics` | read | Prometheus metrics: the server, its number of stacks, and when each stack last had each event, such as `apply.rolled_back`. Give Prometheus a read token with `authorization.credentials_file` |
| `GET /api/v1/events/stream?after=0` | read | the same events live, as server-sent events; reconnect with the last ID (`Last-Event-ID`), as a stream ends after 10 minutes so the token is checked again |
| `POST /api/v1/alerts` | notify | an alert from another tool, such as Dozzle, to the fleet's notification channels: `{"source", "rule", "container", "host", "detail"}` → `202`; see [notifications](notifications.md#alerts-from-other-tools) |
| `GET /api/v1/jobs/{id}` | read | a job's state (`running`, `succeeded`, `failed`), error and log |
| `GET /api/v1/stacks/{name}/history?n=20` | read | the last `n` commits (at most 200) that touched the stack, newest first; `applied` marks the revision running now |

- **Stacks a token doesn't cover:** a token limited to stacks gets `404` for the others, the same as for a stack that doesn't exist.
- **Fresh reads:** every request reads the fleet repository again, so edits and pulls show up without a restart.
- **One operation at a time:** plans fetch template sources and render, so they run one after another.
- **Errors:** a stack that can't be read gets `500` with a generic message. The details (paths, git output) only go to the server's log.

## Signing in

People sign in with passkeys. Turn them on with `serve --passkey-domain fleet.example.org`; `--passkey-origins` lists the web UI's exact origins, by default `https://<domain>`. To try it locally, use `--passkey-domain localhost --passkey-origins http://localhost:PORT`, with the UI's port.

1. **First user:** with no users yet, the server prints a one-time setup token to its log. `POST /api/v1/auth/setup/begin` takes it with a name, and `…/setup/finish?ceremony=ID` registers the passkey and signs in.
2. **Signing in:** `POST /api/v1/auth/login/begin`, then `…/login/finish?ceremony=ID`. It asks for no name, so it doesn't reveal who has an account.
3. **More devices:** `POST /api/v1/auth/passkeys/begin` and `…/finish`, signed in. Register a second one: a lost passkey is otherwise only recovered on the server.
4. **`GET /api/v1/auth/status`** says whether setup is needed and who is signed in. **`POST /api/v1/auth/logout`** signs out.
5. **Codes as a fallback,** for when no passkey is at hand (TOTP, optional per user):
   - **Turning on:** signed in, `POST /api/v1/auth/totp/begin` returns a secret and an `otpauth://` URL for an authenticator app. `…/totp/finish?ceremony=ID` with `{"code"}` proves it works and turns it on.
   - **Signing in:** `POST /api/v1/auth/totp/login` with `{"name", "code"}`.
   - **Turning off:** `POST /api/v1/auth/totp/remove`, signed in.

- **Sessions:** a cookie that's `HttpOnly`, `Secure` and `SameSite=Strict`, lasting 12 hours. They're kept in memory as hashes, so a restart signs everyone out.
- **Same site:** the UI and the API must be on the same site (`ui.example.org` and `api.example.org` are), or the browser won't send the cookie. Changes also need an allowed `Origin`.
- **A person can do everything,** on every stack, and is named as the author of commits. Tokens stay for programs. A request with a bearer token ignores any cookie.
- **Removing a user** ends their sessions at once.
- **Lost a passkey:** on the server, `nepfleet user remove NAME` and restart. With no users left, a new setup token is printed.
- **Lockout:** failed sign-ins count towards the same lockout as tokens. As with tokens, a real passkey gets through from a locked-out address; guessing the setup token doesn't.
- **PIN or biometric:** passkeys must verify the user, as they are the only factor. A security key without a PIN is refused.
- **Codes are weaker than passkeys:** they can be phished, a code alone signs in as that person, and the secret is stored in `users.json` (owner-only). Passkeys alone are safer, so only turn codes on if you need them.
- **Guessing codes:** each works once. Failures count towards the address's lockout, and 5 for one user block that user's codes for 15 minutes from anywhere. Anyone can trigger that block, but it never affects passkeys.

## Signing in through an identity provider

Pocket ID, Authelia, Authentik and other OpenID Connect providers can sign people in, with or without passkeys:

```sh
NEPFLEET_OIDC_CLIENT_SECRET=… nepfleet serve \
  --oidc-issuer https://id.example.org \
  --oidc-client-id nepfleet \
  --oidc-redirect-url https://api.example.org/api/v1/auth/oidc/callback \
  --oidc-allow ada@example.org,group:fleet-admins \
  --allowed-origins https://ui.example.org
```

- **At the provider:** register a confidential client with that redirect URL.
- **`--oidc-allow` is required,** as a provider often has more accounts than should run your fleet. It takes email addresses the provider has verified, `sub:<subject>`, and `group:<name>` from the `groups` claim. Prefer `sub:` or a group when the provider lets people change their email. Anyone on the list can do everything.
- **Signing in:** send the browser to `GET /api/v1/auth/oidc/login?return=<URL>`, with a URL in an allowed origin. It uses the authorization code flow with PKCE, and comes back with the same session cookie as passkeys.
- **Who someone is:** the provider's subject, never a name or email. A name that's taken gets a number.
- **Their only way in:** people who sign in this way can't add a passkey or codes, so the provider and the list keep deciding.
- **Removing someone:** take them off the list, then `nepfleet user remove NAME`, which ends their sessions. Off the list alone stops new sign-ins, but a session lasts up to 12 hours.
- **A first user through OIDC closes passkey setup,** as the setup token is only for an empty server.
- **Claims:** email and groups are read from the ID token, or from the provider's UserInfo endpoint if it leaves them out.
- **Same site:** like passkeys, this needs the UI and the API on the same site, or the browser won't send the session cookie.
- **A restart** ends sign-ins underway, which then have to start again.

## TLS

Tokens must not cross a network in the clear, so a plain-HTTP address beyond loopback is refused. Choose one:

| Flags | For |
|---|---|
| `--tls-cert FILE --tls-key FILE` | Your own certificate, e.g. from your ACME client. Renewed files are picked up within a minute, without a restart. If a renewed pair can't be read, the last good one stays. |
| `--tls-domain api.example.org` | A certificate from Let's Encrypt, kept in `<state>/api/acme`. It uses the TLS-ALPN challenge, so the domain must reach this server on port 443 (`--listen :443`). Using it accepts Let's Encrypt's terms. |
| `--behind-proxy` | A reverse proxy in front terminates TLS, such as the bundled Caddy. The Docker image uses this by default. Add `--trusted-proxies` so the lockout counts clients, not the proxy. |

With TLS on, the server allows TLS 1.2 and newer and sends HSTS.

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

## Rolling back

`POST …/rollback` takes a full commit ID from `GET …/history`; other commits get `409`. The job restores the stack's directory, plans it, commits, and applies.

- **Nothing half done:** if the plan has destructive changes you didn't confirm, or anything else fails before the commit, the directory goes back as it was. The stack's directory must have no uncommitted changes.
- **After the commit:** if the apply itself fails, the commit stays, and the job says so. Roll back again, or fix it forward.

## Commits

Changes over the API are commits in the fleet repository, authored by the token's name (`ci <ci@tokens.nepfleet.invalid>`). This covers rollbacks, and what an apply writes: `lock.yaml` and generated secrets. git's own identity stays the committer, and signing follows git's config, so a server set up to sign needs its key available, or the commit fails. A failed commit after an apply only warns in the job's log; the files are there to commit by hand.
