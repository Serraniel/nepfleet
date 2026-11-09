# Security model

NepFleet controls Docker, and **Docker access is root access** to the host. A compromised NepFleet means every managed host is compromised too. The design therefore keeps the attack surface small and requires several independent failures before damage is possible.

## Defaults

- The API is **off**. `nepfleet serve` binds to `127.0.0.1` and refuses any other address without TLS (see [the API reference](api.md#tls)).
- A NepFleet with a Docker socket is root on that host, so treat its tokens accordingly.
- Templates cannot execute host commands (see [decisions](decisions.md#d10-templates-are-data-not-code)).
- Secrets are stored only encrypted (SOPS and age). The decryption key lives on the server, not in the repository.
- `nepfleet init` installs a pre-commit hook that rejects `*.secret.yaml` files unless they are fully SOPS-encrypted (every value `ENC[…]`). A hook can be skipped (`--no-verify`, or a fresh clone without `core.hooksPath`), so the real guarantee is running `nepfleet secret check` in the fleet repository's CI.

## Exposing the API

Sign-in is new, so **think twice before exposing it to the internet**. Loopback, the LAN or a VPN are safer.

Before it can be public, it needs:

| Requirement | State |
|---|---|
| TLS, terminated by NepFleet or a reverse proxy | Done: plain HTTP beyond loopback is refused ([TLS](api.md#tls)) |
| CORS allowlist with exact origins; `*` is rejected at start | Done |
| Lockout after failed authentication | Done, for tokens and sign-in |
| Passkeys ([#33](https://github.com/Serraniel/nepfleet/issues/33)) | [Done](api.md#signing-in) |
| TOTP as a fallback ([#33](https://github.com/Serraniel/nepfleet/issues/33)) | [Done](api.md#signing-in), optional per user |
| OIDC ([#34](https://github.com/Serraniel/nepfleet/issues/34)) | [Done](api.md#signing-in-through-an-identity-provider), optional |
| Every state change recorded as a commit by the authenticated person ([#37](https://github.com/Serraniel/nepfleet/issues/37)) | Partly: rollbacks and what applies write are commits, authored by the person or token. An apply with nothing to record makes none. |
| The server refusing to start publicly without the above | Not yet: there is no "public" mode to refuse |

## API tokens

Programs authenticate with API tokens: scripts, CI, a Homepage widget, the UI while it is being developed. People sign in with passkeys or OIDC.

```sh
nepfleet token create --name ci --scope read --stack web,db --expires 30d   # prints the token once
nepfleet token list
nepfleet token revoke <id>
```

- **Scopes:** `read` (stacks, plans, history, templates), `apply` (also apply and roll back), `edit` (also change stack files, as commits: what runs, not only re-apply it), `admin` (reserved for tokens and settings over the API). Each includes the ones before it, and `--stack` limits a token to some stacks. An `admin` token can't be limited to stacks.
- **`notify`** stands apart: it only sends alerts (`POST /api/v1/alerts`) and reads nothing, for tools such as Dozzle that keep the token in their own config. Only it and `admin` may send alerts.
- **Expiry:** every token has one.
- **Storage:** tokens look like `nft_…`, so secret scanners can find leaked ones. Only an HMAC of each token is stored, in `<state>/api/tokens.json`, with a key kept next to it (`token.key`). Both files are owner-only. A copy of `tokens.json` without `token.key` (a partial backup, a leak) doesn't let anyone test guesses; anyone who can read both can, so keep the state directory private. A damaged `token.key` is never replaced on its own, as that would silently invalidate every token.
- **Requests:** `Authorization: Bearer nft_…`. Missing, unknown and expired tokens get the same `401`.
- **Lockout:** after 10 failures in a minute, the client address gets `429` for a minute. Behind a reverse proxy, start `serve --trusted-proxies <CIDR>`, so the client named in `X-Forwarded-For` counts rather than the proxy. Otherwise one attacker would lock everyone out. A valid token always gets through, even from a locked-out address.
- **Open endpoints:** only `/healthz` and `/api/v1/info`. The endpoints are listed in [the API reference](api.md).

## Threat model

Reviewed against the code on 2026-11-07. It covers what exists: one server on one host, with the CLI and the token API. Agents and multi-host connections don't exist yet and need their own review ([#41](https://github.com/Serraniel/nepfleet/issues/41), [#42](https://github.com/Serraniel/nepfleet/issues/42)).

**What it protects:** the Docker socket (root on the host), the age key (every secret of the fleet), and the fleet repository (what runs).

| Attacker | What stops them | What remains |
|---|---|---|
| Someone reading the server's log before setup | The token is one-time, and only works while no user exists. | **Whoever reads the log first can create the first user:** complete setup right after starting. |
| A phishing site | Passkeys are bound to the site's domain, so they don't work on a look-alike. | Nothing specific. |
| Someone on the network without a token | Every endpoint but `/healthz` and `/api/v1/info` needs one. Tokens have 256 random bits. TLS beyond loopback. Lockout. | `/api/v1/info` shows the server's name and version; `--name` changes the name. |
| A stolen `read` token | Plans mask secrets. No changes possible. Expiry, and `--stack` limits. | It reads the fleet's structure, images and non-secret values. Revoke it with `nepfleet token revoke`. |
| A stolen `notify` token | It reads nothing and changes nothing. Alerts are capped in size, and deduplicated like every notification. Expiry. | **It can send false alerts,** or many different ones, to the fleet's channels. Revoke it with `nepfleet token revoke`. |
| A stolen `edit` token | Edits are commits, named after the token, and the server log records them. Only a stack's own files can be changed (no encrypted values, no `lock.yaml`, nothing outside the stack, no symlinks). Expiry, and `--stack` limits, which also refuse new references to other stacks' values and secrets. Edits don't apply by themselves. | **It can change what runs:** with `apply` included, it can deploy any container on that host, and Docker access is root. Give it out sparingly, with short expiry. |
| A stolen session (a person) | Editing needs a sign-in or passkey verification within the last 5 minutes, so a session alone can't change what runs once that time is up. Sessions end after 12 hours. | Within those 5 minutes, what an `edit` token can do. |
| A stolen `apply` token | Only what is in the fleet repository gets applied, and destructive changes need confirming. The server log records each apply. | **It can redeploy, and by timing it, disrupt stacks.** It can't change files: that takes `edit`. Job logs show server paths. |
| A malicious web page in the browser | Tokens go in a header, never a cookie, so a page can't use them by itself. CORS allows exact origins only. The UI keeps tokens in memory or this tab's session storage. | A script running in the UI's origin could read a token: keep the UI on its own origin. |
| A malicious template or compose file | Templates are data and run no host commands. Plans show every change before it applies. | **A compose file can mount the host or run privileged, which is root.** Reviewing plans and choosing template sources is the defence: NepFleet doesn't restrict what compose allows. |
| Another user on the host | The `.env` file, tokens, key files and each stack's working directory are owner-only. | Whoever is in the `docker` group is root anyway. |
| Someone with a copy of the repository | Secrets are SOPS-encrypted, and a pre-commit hook and `nepfleet secret check` catch plaintext. | Hooks can be skipped: run the check in CI. |
| A compromised container | Socket proxies give system stacks read-only Docker access. Telemetry hosts are blocked by name. | **Outbound traffic is otherwise open**, so a compromised app can phone home ([#127](https://github.com/Serraniel/nepfleet/issues/127)). The bundled Caddy and Dozzle stacks see what their socket proxies allow. |

**Known gaps to accept or fix:**

- **Token files and the key sit together:** whoever reads the state directory can test token guesses and read the fleet's plans. It is the same trust as the server's own user.
- **An apply with nothing to record makes no commit:** the server log is what records who applied, and it can be edited by whoever runs the server. ([#37](https://github.com/Serraniel/nepfleet/issues/37)).
- **Commit authenticity:** NepFleet applies what is in the working tree, and doesn't verify commit signatures of the fleet repository.
- **Images:** they are pinned by digest once applied, but signatures aren't verified before updating.

## Reporting

See [SECURITY.md](../SECURITY.md).
