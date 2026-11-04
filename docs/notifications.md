# Notifications

NepFleet sends events to any number of channels. Channels are configured as [shoutrrr](https://github.com/nicholas-fedor/shoutrrr) URLs, so Discord, Slack, Telegram, Matrix, Microsoft Teams, ntfy, Gotify, Pushover, e-mail and generic webhooks all work without NepFleet-specific code.

```yaml
# fleet.yaml
spec:
  notifications:
    channels:
      discord: { url: "discord://TOKEN@WEBHOOK_ID" }      # store as a secret, see below
      phone:   { url: "ntfy://ntfy.example.org/nepfleet" }
      ci:      { url: "generic+https://hooks.example.org/nepfleet", format: json }
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

Channel URLs contain tokens, so they go into `fleet.secret.yaml`, which is encrypted with SOPS, and are referenced by name. Run `nepfleet notify test <channel>` to send a test message.

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

Discord, Slack and Teams receive a formatted message with a colour for the severity and a link to the stack in the UI. A `generic` webhook with `format: json` receives the raw event and an `X-NepFleet-Signature` HMAC header, so the receiver can verify where the event came from.

Uptime alerts stay in Uptime Kuma, which has its own notification integrations. NepFleet does not duplicate them.
