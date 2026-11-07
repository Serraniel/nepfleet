# Monitoring

Three things, for three questions:

| Question | Use | How |
|---|---|---|
| Is it up? | Uptime Kuma | the `uptime-kuma` template; templates label their monitors |
| What happened? | Notifications and the event feed | [notifications](notifications.md), `nepfleet watch` |
| How is it doing over time? | Prometheus and Grafana, or Beszel | the templates below |

## Metrics

Install the `prometheus` template, then the `grafana` template.

- **`prometheus`** scrapes itself, this host (node-exporter), and every service with a `nepfleet.metrics.port` label on the ingress network, labelled with `stack` and `service`. It isn't published, as it has no login.
- **`grafana`** has Prometheus as its data source, at `http://prometheus:9090` by default. It has no dashboards yet, so import what you need.
- **`beszel`** is the lighter choice for host and container metrics, with history and alerts. Each watched host runs an agent, which the hub's Add System dialog explains.

Templates declare what they expose with `supports.metrics` and [labels](spec/template.md). Not many do yet.

## NepFleet's own metrics

`nepfleet serve` answers `GET /metrics` with a [read token](api.md):

```yaml
# In a Prometheus you configure yourself
scrape_configs:
  - job_name: nepfleet
    authorization:
      credentials_file: /run/secrets/nepfleet-token
    static_configs:
      - targets: ["nepfleet.example.org:7480"]
```

- `nepfleet_info` and `nepfleet_stacks`.
- `nepfleet_stack_last_event_timestamp_seconds{stack, event}`: when a stack last had an event. These are times, not counters, as events are kept only for a while.

An alert on a recent rollback:

```yaml
- alert: StackRolledBack
  expr: time() - nepfleet_stack_last_event_timestamp_seconds{event="apply.rolled_back"} < 3600
```

The `prometheus` template doesn't scrape this by itself, as it needs the token.

**Not yet:** dashboards and alert rules shipped by templates, per-container metrics through cAdvisor, and metrics on swarm.
