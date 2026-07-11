# AlertLens

Alertmanager-first HolmesGPT RCA companion for Slack.

AlertLens is designed to keep the existing **Alertmanager -> Slack** notification path as the source of truth. Alertmanager posts the authoritative alert message to Slack; AlertLens listens to that Slack message, enriches it, asks HolmesGPT for RCA, and posts concise analysis into the same thread.

The firing-alert path is implemented; resolved alerts, ad-hoc questions, and thread follow-ups are being built in later verified milestones. See the [approved design](docs/superpowers/specs/2026-07-11-alertlens-design.md) for the complete MVP contract.

## Slack app

Use a dedicated Slack App while AlertLens and Vigil run in parallel. Enable Socket Mode and configure:

- app token scope: `connections:write`
- bot scopes: `app_mentions:read`, `channels:history`, `chat:write`, `reactions:read`, `reactions:write`
- event subscriptions: `message.channels`, plus `app_mention` for the upcoming ad-hoc/follow-up milestone

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

The process exposes `/healthz` and `/readyz` on port 9090 by default.

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
