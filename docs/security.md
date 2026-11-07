# Security model

NepFleet controls Docker, and **Docker access is root access** to the host. A compromised NepFleet means every managed host is compromised too. The design therefore keeps the attack surface small and requires several independent failures before damage is possible.

## Defaults

- The API is **off**. `nepfleet serve` binds to `127.0.0.1` and warns if it is bound anywhere else without TLS.
- Agents open no ports. They dial the server over mTLS with short-lived certificates (24 h), and certificates are revoked when a host is removed.
- The server holds no Docker socket unless it is also an agent.
- Templates cannot execute host commands (see [decisions](decisions.md#d10-templates-are-data-not-code)).
- Secrets are stored only encrypted (SOPS and age). The decryption key lives on the server, not in the repository.
- `nepfleet init` installs a pre-commit hook that rejects `*.secret.yaml` files unless they are fully SOPS-encrypted (every value `ENC[…]`). A hook can be skipped (`--no-verify`, or a fresh clone without `core.hooksPath`), so the real guarantee is running `nepfleet secret check` in the fleet repository's CI.

## Exposing the API to the internet

These are required, and the server refuses to start without them once the API is public:

1. TLS, terminated by NepFleet itself or by the bundled Caddy.
2. Authentication with passkeys (WebAuthn), with TOTP as a fallback, or OIDC. Passwords alone are never enough.
3. A CORS allowlist containing exact origins only. `*` is rejected.
4. Rate limiting and lockout on authentication endpoints.

Every state change is a git commit that records the authenticated user, which gives an audit log you cannot silently edit.

## API tokens

Programs authenticate with API tokens: scripts, CI, a Homepage widget, the UI while it is being developed. People sign in with passkeys or OIDC.

```sh
nepfleet token create --name ci --scope read --stack web,db --expires 30d   # prints the token once
nepfleet token list
nepfleet token revoke <id>
```

- **Scopes:** `read` (stacks, plans, history), `apply` (also apply and roll back), `admin` (also tokens and settings). Each includes the ones before it, and `--stack` limits a token to some stacks.
- **Expiry:** every token has one.
- **Storage:** tokens look like `nft_…`, so secret scanners can find leaked ones. Only an HMAC of each token is stored, in `<state>/api/tokens.json`, with a key kept next to it (`token.key`). Both files are owner-only. A copy of `tokens.json` without `token.key` (a partial backup, a leak) doesn't let anyone test guesses; anyone who can read both can, so keep the state directory private. A damaged `token.key` is never replaced on its own, as that would silently invalidate every token.
- **Requests:** `Authorization: Bearer nft_…`. Missing, unknown and expired tokens get the same `401`.
- **Lockout:** after 10 failures in a minute, the client address gets `429` for a minute. Behind a reverse proxy, start `serve --trusted-proxies <CIDR>`, so the client named in `X-Forwarded-For` counts rather than the proxy. Otherwise one attacker would lock everyone out. A valid token always gets through, even from a locked-out address.
- **Open endpoints:** only `/healthz` and `/api/v1/info`. The endpoints are listed in [the API reference](api.md).

## Reporting

See [SECURITY.md](../SECURITY.md).
