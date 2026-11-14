# Trusted TLS on a home network

Services at home can have real, trusted certificates without being reachable from the internet and without port forwarding. Caddy proves that you own the domain through a DNS record (the ACME DNS-01 challenge) instead of an HTTP request from Let's Encrypt.

## What you need

- **A domain you own,** for example `example.org`. Home services can use a subdomain such as `home.example.org`.
- **An API token for the domain's DNS:** Cloudflare, Hetzner and deSEC are supported by the NepFleet Caddy image. Give the token as little access as possible; see [DNS API tokens](#dns-api-tokens) for the minimum per provider.
- **A DNS record that points the names at the host,** for devices on the LAN only (see below).

## Set up Caddy

```sh
nepfleet enable ingress
```

The wizard asks for:

- `ACME_EMAIL`: Let's Encrypt contacts you there about expiring certificates.
- `CADDY_DNS_PROVIDER`: `cloudflare`, `hetzner` or `desec`.
- `DNS_API_TOKEN`: stored encrypted in the stack's secrets.

For a home network, also set (in `stack.yaml` or the wizard):

- `WILDCARD`: `on`, and `WILDCARD_DOMAIN`: your home domain, such as `home.example.org`. Caddy then gets one certificate for `*.home.example.org`, and every stack under it uses that one.

Why it matters: every certificate Let's Encrypt issues is published in public Certificate Transparency logs, which anyone can search (for example on crt.sh). With one certificate per name, `vaultwarden.home.example.org`, `jellyfin.home.example.org` and the rest form a public list of what runs at your home. With the wildcard, only `*.home.example.org` appears. A wildcard covers one level, so `a.b.home.example.org` still gets its own certificate.

Apply it with `nepfleet apply caddy`. Caddy then gets every certificate through DNS-01: it creates a `_acme-challenge` TXT record with the token, Let's Encrypt checks it, and the record is removed again. Nothing needs to reach your network from outside.

Stacks keep using their domain as usual (`DOMAIN` in the wizard, the `caddy` label in templates), for example `jellyfin.home.example.org`.

## Make the names resolve at home

The certificate is public, but the address should only be known on your LAN. Two common ways:

- **Local DNS records (recommended):** in AdGuard Home, Pi-hole or your router, point `*.home.example.org` (or each name) at the host's LAN address, for example `192.168.1.20`. Devices that use that DNS server reach the service directly, and no DNS record publishes your network. Certificates are public, though: see the wildcard above.
- **Split DNS / public record to a private address:** an `A` record `*.home.example.org → 192.168.1.20` in the public zone also works, because the address is only reachable inside your LAN. It does publish your internal address, and some routers block private answers ("DNS rebinding protection"), so allow the domain there.

## Check it

- `nepfleet doctor` on the host checks that ports 80 and 443 are free for Caddy.
- `docker logs caddy-caddy-1` shows `certificate obtained successfully` for each name. An error mentioning `_acme-challenge` almost always means the token lacks permission for that zone.
- In the browser, the lock icon shows a Let's Encrypt certificate for the name.

## Notes

- **Wildcards:** DNS-01 is the only way to get wildcard certificates, so the wildcard needs a DNS provider. Without one, Caddy issues a certificate per name, and each name shows up in the public logs.
- **Rate limits:** Let's Encrypt allows 50 certificates per registered domain per week. For many services or frequent rebuilds, keep Caddy's `data` volume (it holds the certificates) in backups so it doesn't request them again.
- **Public services:** services reachable from the internet need nothing special; DNS-01 works for them too.

## DNS API tokens

Caddy only creates and removes the temporary `_acme-challenge` TXT records that prove you own the domain. Its token needs nothing more. The minimum per provider:

**Cloudflare** (My Profile → API Tokens → Create Token → *Edit zone DNS*):
- **Permissions:**
  - **Zone → DNS → Edit**: comes with the template.
  - **Zone → Zone → Read**: add it. Caddy looks up the zone with it.
- **Zone resources:** **Include → Specific zone → your domain**, not all zones.
- **Expiration:** a year, with a reminder to renew it. When it lapses, renewals fail and Caddy reports it, before the certificates themselves run out.
- **Client IP address filtering:** the host's public IP if it is fixed (a VPS). Leave it empty for a host at home whose address changes.
- Nothing else: no Workers, account or "read all resources" permissions.

**Hetzner** (Hetzner Console, the Cloud DNS API):
- **Token:** a project API token with **Read & Write** (Project → Security → API tokens).
- **Scope:** Hetzner can't limit a token to one zone, so it can change every resource of the project. Keep the DNS zone in a project of its own, with no servers in it.

**deSEC** (Token management):
- **Token:** a new token without *manage tokens* and without *create/delete domains* (`perm_manage_tokens`, `perm_create_domain` and `perm_delete_domain` off). A token that may manage tokens can lift its own limits.
- **Limits:** give it token policies that only allow TXT records of your domain:
  1. A default policy that denies writing: domain, subname and type empty, *write* off.
  2. One that allows TXT: your domain, subname empty, type `TXT`, *write* on.
- **Optional:** set *allowed subnets* to the host's public IP if it is fixed.

The token goes into the Caddy stack's `DNS_API_TOKEN`, stored encrypted (`nepfleet secret set caddy DNS_API_TOKEN`, or the app's Secrets on the stack's edit page). To replace it, set the new one there and apply the stack; then revoke the old one at the provider.

