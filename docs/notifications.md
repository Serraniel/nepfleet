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
| `container.crashed` / `container.crash_loop` (3 crashes in 10 minutes) | warning / error |
| `container.oom` / `container.unhealthy` | error / warning |
| `security.login_failed_repeatedly` / `security.token_created` | warning / info |

Discord, Slack, Teams, ntfy and Telegram receive the title and a colour or priority for the severity. The message ends with the stack and host. For applies, it also lists what changed (`changed compose.yaml, changed image nginx:1.29`, …). With `notifications.uiURL` set, for example `uiURL: https://nepfleet.example.org`, messages about a stack link to `<uiURL>/stacks/<name>`. Signed webhooks get the link as `url`.

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

## Event feed

Every event is also recorded on the host, in `<state>/events/events.jsonl`, whether or not channels are configured. The last 1000 stay, and clients catch up with [`GET /api/v1/events`](api.md). The file is owner-only, because messages can hold a container's last log lines. Those lines can contain whatever an app logs, including secrets, so the read scope to this feed is worth treating like access to the logs.

## Container problems

`nepfleet watch` follows Docker's events on a host and reports OOM kills, crashes, crash loops and failed health checks. Each message names the stack and includes the container's last log lines.

- **Not crashes:** stops that were asked for, by `docker stop`, an apply or a restart (a kill event first, or exit codes 0 and 143).
- **No floods:** each kind of problem is sent at most once per container per 30 minutes.
- **How to run it:** as a service next to Docker, for example a systemd unit or the NepFleet container. Run it on every host whose containers you want watched.

Uptime alerts stay in Uptime Kuma, which has its own notification integrations. NepFleet does not duplicate them.
