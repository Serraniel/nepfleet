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
- **`beszel`** is the lighter choice for host and container metrics, with history and alerts. Each watched host runs a `beszel-agent` stack. It pairs with a hub in the same fleet by itself: in the agent's stack.yaml, take `TOKEN` from the hub's `AGENT_TOKEN` secret and `PAIR_PASSWORD` from its `PAIR_PASSWORD` secret (`{from: <hub stack>, secret: …}`). The agent then shows up in the hub under the host's name. A hub outside the fleet still works with `KEY` and `TOKEN` copied from its Add System dialog.

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
