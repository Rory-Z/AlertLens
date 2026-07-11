# AlertLens

Alertmanager-first HolmesGPT RCA companion for Slack.

AlertLens is designed to keep the existing **Alertmanager -> Slack** notification path as the source of truth. Alertmanager posts the authoritative alert message to Slack; AlertLens listens to that Slack message, enriches it, asks HolmesGPT for RCA, and posts concise analysis into the same thread.

The firing, resolved, ad-hoc, and thread follow-up paths are implemented. See the [approved design](docs/superpowers/specs/2026-07-11-alertlens-design.md) for the complete MVP contract.

Current alert behavior:

- marked Slack notifications are confirmed against Alertmanager before processing
- firing alerts receive one HolmesGPT RCA per active session
- confirmed resolution replies in the original thread and adds `large_green_circle`
- Slack event-ID deduplication and thread/session state survive a single-replica restart
- Watchdog updates metrics without invoking HolmesGPT
- a top-level `@AlertLens` creates an ad-hoc thread
- an explicit mention in a known alert thread reuses its alert and conversation context
- an explicit mention in an unknown thread creates ad-hoc context on that thread
- human messages without an explicit mention are ignored

## Slack app

Use a dedicated Slack App while AlertLens and Vigil run in parallel. Enable Socket Mode and configure:

- app token scope: `connections:write`
- bot scopes: `app_mentions:read`, `channels:history`, `chat:write`, `reactions:read`, `reactions:write`
- event subscriptions: `message.channels` and `app_mention`

Do not share Vigil's app token: simultaneous Socket Mode clients compete for envelopes.

## Development

Go 1.25 or newer is required. The service reads configuration from the environment; the required names are:

- `SLACK_BOT_TOKEN`
- `SLACK_APP_TOKEN`
- `SLACK_ALERT_CHANNELS`
- `ALERTMANAGER_URL`
- `HOLMESGPT_URL`

Use non-production placeholder credentials for the current foundation:

```bash
SLACK_BOT_TOKEN=xoxb-test \
SLACK_APP_TOKEN=xapp-test \
SLACK_ALERT_CHANNELS=C1 \
ALERTMANAGER_URL=http://alertmanager:9093 \
HOLMESGPT_URL=http://holmes:5050 \
STATE_PATH=/tmp/alertlens-state.json \
go run ./cmd/alertlens
```

The process exposes `/healthz`, `/readyz`, and Prometheus `/metrics` on port 9090 by default.

## Deployment

Create a dedicated Secret whose keys are `bot-token` and `app-token`; do not put either token in Helm values. The chart requires an RWO PVC. If the cluster has no default StorageClass, set `state.storageClass` explicitly (the FlowMQ dev cluster uses `gp3`).

The default NetworkPolicy permits DNS and Slack HTTPS but intentionally does not guess the internal network ranges for HolmesGPT and Alertmanager. Add the smallest pod or service CIDRs and the ports used by the configured URLs:

```yaml
state:
  storageClass: gp3

networkPolicy:
  internalEgress:
    - cidr: <alertmanager-pod-or-service-cidr>
      ports: [9093]
    - cidr: <holmes-pod-or-service-cidr>
      ports: [80]
```

For the FlowMQ dev cluster, the service URLs are:

```text
http://vmalertmanager-victoria-metrics-k8s-stack.victoria.svc:9093
http://holmes-holmes.holmes.svc:80
```

Internal endpoint IPs can change, so discover the current CIDRs at deployment time instead of committing them. A real smoke deployment also needs an image that the cluster can pull and a separate Slack App/test channel.

Alert on a missing Watchdog without depending on AlertLens to evaluate the condition:

```promql
absent(alertlens_watchdog_last_seen_timestamp)
or time() - alertlens_watchdog_last_seen_timestamp > 300
```

## Verification

```bash
test -z "$(gofmt -l .)"
go vet ./...
go test -race -coverprofile=coverage.out ./...
go build ./cmd/alertlens
helm lint charts/alertlens \
  --set slack.existingSecret=alertlens-slack \
  --set-string 'slack.alertChannels[0]=C1' \
  --set alertmanagerURL=http://alertmanager:9093 \
  --set holmesURL=http://holmes:5050
```

CI rejects statement coverage below 90% and also runs Helm unit tests and a container build.
