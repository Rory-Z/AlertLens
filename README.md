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

Render the canonical Slack App Manifest for the target environment:

```bash
./scripts/render-slack-manifest dev  > /tmp/alertlens-dev.yaml
./scripts/render-slack-manifest prod > /tmp/alertlens-prod.yaml
```

Import the chosen file in Slack to create the app. Generate a separate app-level token with `connections:write`, then install the app to the workspace to obtain its `xoxb` bot token and invite the app to the monitored channel. Store the tokens in the existing Kubernetes Secret as `app-token` and `bot-token`, respectively.

Use a dedicated Slack App while AlertLens and Vigil run in parallel. The manifest configures Socket Mode plus:

- bot scopes: `app_mentions:read`, `channels:history`, `groups:history`, `chat:write`, `reactions:read`, `reactions:write`
- event subscriptions: `message.channels`, `message.groups`, and `app_mention`

The app-level `connections:write` scope is not part of the manifest; add it when generating the separate app-level token described above.

Do not share Vigil's app token: simultaneous Socket Mode clients compete for envelopes.

## Development

Go 1.25 or newer is required. The service reads configuration from the environment; the required names are:

- `SLACK_BOT_TOKEN`
- `SLACK_APP_TOKEN`
- `SLACK_ALERT_CHANNELS` (comma-separated public or private channel IDs)
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

The default NetworkPolicy permits DNS and any destination on TCP 443; native Kubernetes NetworkPolicy cannot enforce Slack FQDNs. Use a CNI FQDN policy or egress proxy when strict Slack-only HTTPS is required. Add the namespaces and ports used by HolmesGPT and Alertmanager:

```yaml
state:
  storageClass: gp3

networkPolicy:
  internalEgress:
    - namespace: victoria
      ports: [9093]
    - namespace: holmes
      ports: [80]
```

For the FlowMQ dev cluster, use `~/.kube/flowmq-dev-tiger.yaml` as the
kubeconfig. The service URLs are:

```text
http://vmalertmanager-victoria-metrics-k8s-stack.victoria.svc:9093
http://holmes-holmes.holmes.svc:80
```

Namespace selectors keep this access stable when internal endpoint IPs change. A real smoke deployment also needs an image that the cluster can pull and a separate Slack App. The dev E2E below shares Vigil's dev channel, so duplicate replies and reactions are expected.

## Dev E2E

The opt-in E2E exercises the ordinary AlertLens image against the real dev
Alertmanager, HolmesGPT, and Slack workspace. It uses AlertLens's dedicated
Slack App in Vigil's dev channel; never reuse Vigil's app token. Install and
invite the AlertLens App first, then create a Secret with `bot-token` and
`app-token` keys using the normal secret-management workflow. The Makefile
never creates, updates, or deletes that Secret.

The defaults are:

| Variable | Default |
| --- | --- |
| `KUBECONFIG` | `~/.kube/flowmq-dev-tiger.yaml` |
| `IMAGE` | `ghcr.io/rory-z/alertlens:latest` |
| `E2E_NAMESPACE` | `alertlens-e2e` |
| `E2E_RELEASE` | `alertlens-e2e` |
| `E2E_SLACK_SECRET` | `alertlens-e2e-slack` |
| `E2E_SLACK_CHANNEL` | `C099FMSGNEQ` |
| `E2E_STORAGE_CLASS` | `gp3` |
| `E2E_ALERTMANAGER_NAMESPACE` | `victoria` |
| `E2E_ALERTMANAGER_SERVICE` | `vmalertmanager-victoria-metrics-k8s-stack` |
| `E2E_ALERTMANAGER_URL` | `http://vmalertmanager-victoria-metrics-k8s-stack.victoria.svc:9093` |
| `E2E_ALERTMANAGER_PORT` | `9093` |
| `E2E_ALERTMANAGER_LOCAL_PORT` | `19093` |
| `E2E_HOLMES_NAMESPACE` | `holmes` |
| `E2E_HOLMES_URL` | `http://holmes-holmes.holmes.svc:80` |
| `E2E_HOLMES_PORT` | `80` |

Create the namespace before provisioning the Secret. Every default is a Make
variable and can be overridden on the command line; an exported `KUBECONFIG`
takes precedence over the default. `IMAGE` must use `repository:tag` form;
tagless and digest references are rejected.

```bash
kubectl create namespace alertlens-e2e --dry-run=client -o yaml | kubectl apply -f -

make build
make push
# Or build and push in one step; IMAGE_PLATFORMS is optional:
make build-push
make build-push IMAGE_PLATFORMS=linux/amd64,linux/arm64

make e2e-deploy
make e2e-test
make e2e-undeploy
```

`e2e-deploy` creates the namespace if needed, verifies the external Secret,
forces the configured image to be pulled, applies namespace-based egress, and
waits for the deployment to become Ready. `e2e-test` does not deploy anything:
it verifies the release, reads the bot token from the Secret, and temporarily
port-forwards Alertmanager to the local test process.

The test injects a clearly labelled synthetic alert, waits for the RCA, and
prints an `ACTION REQUIRED` prompt with a direct Slack thread link. Mention
AlertLens in that thread and include the supplied run ID. The runner detects
the follow-up automatically, resolves the alert, and verifies the final thread
reply and reactions. The alert is resolved on normal failure paths; its
one-hour `endsAt` is only a fallback for a forcibly terminated runner. This
interactive test is not run in CI. The runner allows up to 20 minutes for each
complete AlertLens response (including its 15-minute HolmesGPT call limit), 10
minutes for the human step, 7 minutes for resolution, and 60 minutes for the
overall test process.

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
