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

Sent today: `apply.succeeded`, `apply.failed`, `apply.rolled_back`, `update.available` and `update.applied`, plus what `nepfleet watch` and `doctor --security --notify` report. The other events follow with their features.

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
| `egress.denied` (`watch`: a stack in egress mode enforce was refused connections) | warning |
| `alert.<source>` (from another tool, e.g. Dozzle) | warning |

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

## Dedup and quiet hours

```yaml
spec:
  notifications:
    dedupWindow: 1h        # the same event at most once per hour
    quietHours:
      from: "22:00"
      to: "07:00"
      timezone: Europe/Berlin   # default: the host's
```

- **The same event** means the same name, stack, host and title, so two different containers crashing are two events.
- **Quiet hours** hold `info` events back. Warnings and errors always go out at once.
- **The digest** lists what was held, per channel. It goes out with the next event after quiet hours end, or within a minute if `nepfleet watch` is running.
- **The [event feed](#event-feed) has everything:** this only limits messages to channels. A digest that fails to send isn't retried, but its events stay in the feed.
- **Off by default,** and `nepfleet notify test` ignores both.
- **If the state can't be read,** in `<state>/notify`, events go out: too many messages beat a missed one.

## Event feed

Every event is also recorded on the host, in `<state>/events/events.jsonl`, whether or not channels are configured. The last 1000 stay, and clients catch up with [`GET /api/v1/events`](api.md). The file is owner-only, because messages can hold a container's last log lines. Those lines can contain whatever an app logs, including secrets, so the read scope to this feed is worth treating like access to the logs.

## Container problems

`nepfleet watch` follows Docker's events on a host and reports OOM kills, crashes, crash loops and failed health checks. Each message names the stack and includes the container's last log lines.

- **Not crashes:** stops that were asked for, by `docker stop`, an apply or a restart (a kill event first, or exit codes 0 and 143).
- **No floods:** each kind of problem is sent at most once per container per 30 minutes.
- **How to run it:** as a service next to Docker, for example a systemd unit or the NepFleet container. Run it on every host whose containers you want watched.

- **Refused connections:** every 10 minutes, `watch` also reads the logs of stacks in egress mode `enforce`, and reports what they were refused, by name where it saw the lookup (`egress.denied`). The title is the same each time, so dedup holds back repeats until the window passes. `nepfleet egress suggest <stack>` lists everything.

Uptime alerts stay in Uptime Kuma, which has its own notification integrations. NepFleet does not duplicate them.

## Alerts from other tools

Tools such as Dozzle find problems NepFleet can't see, like a log line that says `panic`. They send them to `POST /api/v1/alerts` on `nepfleet serve`, and NepFleet records them in the event feed and sends them to the channels, with the same dedup and quiet hours as its own events:

```sh
nepfleet token create --name dozzle --scope notify --expires 365d
```

```json
{"source": "dozzle", "rule": "panic", "container": "web-app-1", "host": "nas", "detail": "panic: runtime error"}
```

- **A `notify` token** only sends alerts, and reads nothing. Give it to the tool, not a `read` token.
- **The stack** comes from the container's compose name (`<stack>-<service>-<n>`).
- **Dedup:** the same rule on the same container is one event, whatever the line says. A flood of matches is one message per dedup window.
- **Severity:** a warning, so quiet hours don't hold it.
- **Text** is cut to 120 characters per field and 2000 for the detail, without control characters.
- **The `dozzle` template** sends its alerts here when given the API's address and the token.
