# Notifications

NepFleet sends events to any number of channels. Channels are configured as [shoutrrr](https://github.com/nicholas-fedor/shoutrrr) URLs, so Discord, Slack, Telegram, Matrix, Microsoft Teams, ntfy, Gotify, Pushover, e-mail and generic webhooks all work without NepFleet-specific code.

```yaml
# fleet.yaml
spec:
  notifications:
    channels:
      discord: { urlSecret: DISCORD_URL }                       # discord://TOKEN@WEBHOOK_ID
      phone:   { url: "ntfy://ntfy.example.org/nepfleet" }      # no secret in it
      ci:      { urlSecret: CI_HOOK_URL, format: json }
    routes:
      - channels: [discord]
        events: ["*"]
        minSeverity: warning
      - channels: [phone]
        events: [apply.rolled_back, host.update_failed, host.unreachable]
      - channels: [ci]
        events: [apply.*]
        stacks: [forgejo, registry]
```

Channel URLs usually contain tokens. Store them in `fleet.secret.yaml`, which is encrypted with SOPS, with `nepfleet secret set --global DISCORD_URL`, and refer to them with `urlSecret`. Write `url` directly only when the URL holds no secret.

- **Without routes,** every event goes to every channel.
- **Patterns in `events`:** `*`, an exact name, or `apply.*` for a whole group.
- **`nepfleet notify test <channel>`** sends a test message.
- **Errors never show a channel's URL,** and a failing channel never blocks an apply.

Sent today: `apply.succeeded`, `apply.failed` and `apply.rolled_back`. The other events follow with their features.

## Events

| Event | Severity |
|---|---|
| `apply.succeeded` / `apply.failed` / `apply.rolled_back` | info / error / warning |
| `update.available` / `update.applied` | info |
| `update.beyond_track` (a new major version, needs a template change) | info |
| `backup.failed` / `restore.completed` | error / info |
| `host.update_pending` / `host.update_failed` / `host.reboot_required` | info / error / warning |
| `host.unreachable` / `node.drained` | error / info |
| `security.login_failed_repeatedly` / `security.token_created` | warning / info |

Discord, Slack and Teams receive a formatted message with a colour for the severity.

## Signed JSON webhooks

A `generic` webhook with `format: json` receives the event as JSON. Add `signingSecret` to have it signed, so the receiver can check that the event came from NepFleet and wasn't changed:

```yaml
    channels:
      ci: { urlSecret: CI_HOOK_URL, format: json, signingSecret: CI_HOOK_KEY }   # both keys in fleet.secret.yaml
```

The request is a `POST` with the body `{"event", "severity", "stack", "host", "title", "message", "time"}` and these headers:

- `X-NepFleet-Event`: the event name.
- `X-NepFleet-Timestamp`: Unix seconds.
- `X-NepFleet-Signature: sha256=<hex>`: the HMAC-SHA256 of `<timestamp>.<body>`, keyed with the signing secret.

To verify, recompute the HMAC over the raw body, compare it in constant time, and reject timestamps more than a few minutes old to stop replays. In Python:

```python
expected = "sha256=" + hmac.new(key, f"{ts}.".encode() + body, hashlib.sha256).hexdigest()
ok = hmac.compare_digest(expected, request.headers["X-NepFleet-Signature"]) and abs(time.time() - int(ts)) < 300
```

Uptime alerts stay in Uptime Kuma, which has its own notification integrations. NepFleet does not duplicate them.
