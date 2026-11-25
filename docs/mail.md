# Mail

Many apps send mail: password resets, invites, alerts. Set the fleet's SMTP server once, and templates use it, so you don't type the same values into every stack.

```yaml
# fleet.yaml
spec:
  mail:
    host: smtp.example.org
    port: 587                       # default: 587 for starttls, 465 for tls, 25 for none
    user: fleet@example.org
    passwordSecret: MAIL_PASSWORD   # a key in fleet.secret.yaml
    from: "NepFleet <fleet@example.org>"
    tls: starttls                   # starttls | tls | none
```

Store the password encrypted with `nepfleet secret set --global MAIL_PASSWORD`.

- **`starttls`** connects in plain text and upgrades to TLS (port 587). A server that doesn't offer the upgrade is refused.
- **`tls`** connects with TLS from the start (port 465).
- **`none`** sends in plain text, for a relay on the same network. NepFleet's test never sends a password this way.
- **`from`** is the sender address. Without it, the test uses `user` if that is an address.

## From the app

The Notifications page has a Mail form for each connected backend. It writes `spec.mail` to `fleet.yaml`, stores the password encrypted as the fleet secret `MAIL_PASSWORD` (or the key `passwordSecret` already names), and commits both. The password is never shown again: leave it empty to keep it. Changing these settings needs the admin scope and, in a browser session, a recent passkey check, as other secrets do.

**Send a test mail** sends one to an address you give, with the saved settings. Save first. At most 5 test mails go out in 10 minutes. A failure shows the server's reason, never the password.

The API is `GET`, `PUT` and `DELETE /api/v1/mail` and `POST /api/v1/mail/test` (see [api](api.md)).

## In templates

Stacks get the settings as built-ins. All are empty when the fleet has no `spec.mail`.

| Built-in | Value |
|---|---|
| `NEPFLEET_SMTP_HOST` | `host` |
| `NEPFLEET_SMTP_PORT` | `port`, or the default for `tls` |
| `NEPFLEET_SMTP_USER` | `user` |
| `NEPFLEET_SMTP_PASSWORD` | the password; masked in plans and output |
| `NEPFLEET_SMTP_FROM` | `from` |
| `NEPFLEET_SMTP_TLS` | `starttls`, `tls` or `none` |

A template keeps its own SMTP inputs and uses the built-ins as their fallback. Compose fills `${A:-${B}}` with `B` when `A` is empty, so a value set on the stack wins:

```yaml
# compose.yaml of a template with inputs SMTP_HOST, SMTP_PORT, SMTP_USER, SMTP_PASSWORD, SMTP_FROM
environment:
  SMTP_HOST: ${SMTP_HOST:-${NEPFLEET_SMTP_HOST}}
  SMTP_PORT: ${SMTP_PORT:-${NEPFLEET_SMTP_PORT}}
  SMTP_USERNAME: ${SMTP_USER:-${NEPFLEET_SMTP_USER}}
  SMTP_PASSWORD: ${SMTP_PASSWORD:-${NEPFLEET_SMTP_PASSWORD}}
  SMTP_FROM: ${SMTP_FROM:-${NEPFLEET_SMTP_FROM}}
```

- **No input defaults.** An input's `default` is a fixed value: it can't name a built-in, and an input with a default is never empty, so the fallback never applies. Leave `default` off the SMTP inputs and say in the input's description that the fleet's mail settings apply when it is empty.
- **The app's own TLS words.** Apps name TLS modes differently (`force_tls`, `true`, `ssl`). Compose can't translate values, so a template whose app needs other words keeps an enum input for it, or fixes the value in the compose file.
- **The password reaches only stacks that ask.** NepFleet reads it only for stacks whose files mention `NEPFLEET_SMTP_PASSWORD`, and like every built-in it is written to a stack's `.env` only when its files use it. A plain stack that loads all of `.env` (`env_file: .env`) doesn't get it unless it mentions it.
- **Changing the settings** changes the stacks that use them: they apply again (or show a pending plan, with `apply: manual`).

A plain compose stack uses the built-ins the same way: `${NEPFLEET_SMTP_HOST}` in its `compose.yaml`.

## Not yet

A mail relay stack inside the fleet, so apps that want a local SMTP server reach `mail:25`, is planned separately.
