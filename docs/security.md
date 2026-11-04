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

## Reporting

See [SECURITY.md](../SECURITY.md).
