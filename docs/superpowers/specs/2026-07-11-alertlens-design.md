# AlertLens Design

Date: 2026-07-11
Status: Approved

## Purpose

AlertLens is an Alertmanager-first RCA companion for Slack. It keeps the useful
alert-to-analysis path from Vigil without carrying forward Vigil's AI agent,
ArgoCD, GitOps, cron, shell execution, or TypeScript runtime.

```text
Alertmanager -> Slack       authoritative notification and trigger
Slack -> AlertLens          event and thread anchor
AlertLens -> HolmesGPT      read-only investigation
AlertLens -> Slack          RCA and follow-ups in the original thread
```

If AlertLens is unavailable, Alertmanager still notifies Slack.

## Scope

The MVP includes:

- Slack Socket Mode ingestion.
- Automatic RCA for Alertmanager bot messages.
- Resolved updates in the original alert thread.
- Explicit thread follow-ups and top-level `@AlertLens` ad-hoc questions.
- Inline runbooks from `annotations.runbook`.
- Status reactions, Watchdog metrics, health, readiness, and metrics.
- Single-replica recovery from an RWO PVC.

The MVP excludes:

- ArgoCD, GitOps, PR creation, remediation, and arbitrary shell execution.
- Direct Kubernetes, Prometheus, VictoriaLogs, or Git access from AlertLens.
- `annotations.runbook_url`, private repository access, and GitHub MCP setup.
- Scheduled checks, Alertmanager webhooks, PagerDuty, databases, Redis, and
  active-active replicas.

PagerDuty and private Git runbooks remain future work and add no abstractions to
the MVP.

## Technology

AlertLens is written in Go. It is an I/O-bound service with a small number of
HTTP and WebSocket integrations. Go provides a single binary, simple bounded
concurrency, mature Socket Mode support through `slack-go/slack`, and an
official PagerDuty client for future work.

Rust was rejected because its async and integration glue add complexity without
a meaningful benefit for this workload. Trimming Vigil in TypeScript was
rejected because the goal is to remove its historical runtime and structure,
not add another Vigil mode.

## Architecture

```text
Slack Socket Mode
       | immediate ACK
       v
event filter and router ----> in-memory sessions and TTL dedup
       |
       +-- Alertmanager marker -> Alertmanager API
       |                            `-> alerts + inline runbook
       |
       `-- explicit mention
                    |
                    v
             HolmesGPT /api/chat
                    |
          Kubernetes stdout + VictoriaLogs
                    |
                    v
              Slack thread reply

sessions <-> atomic JSON snapshot on RWO PVC
```

Code is kept in a few concrete areas:

```text
cmd/alertlens          startup and dependency assembly
internal/slack         Socket Mode, reactions, and replies
internal/alertmanager  active-alert queries and matching
internal/holmes        /api/chat client
internal/session       memory state, TTLs, and PVC snapshots
internal/service       routing and use-case orchestration
```

No provider interface or factory is introduced solely for future PagerDuty.

## Configuration

Required:

| Name | Meaning |
|---|---|
| `SLACK_BOT_TOKEN` | Slack Web API bot token |
| `SLACK_APP_TOKEN` | Slack Socket Mode app token |
| `SLACK_ALERT_CHANNELS` | Comma-separated monitored channel IDs |
| `ALERTMANAGER_URL` | The one Alertmanager base URL |
| `HOLMESGPT_URL` | HolmesGPT base URL |

Optional defaults:

| Name | Default |
|---|---|
| `STATE_PATH` | `/var/lib/alertlens/state.json` |
| `REPLY_LANGUAGE` | `en` |
| `ALERTMANAGER_TIMEOUT` | `5s` |
| `HOLMESGPT_TIMEOUT` | `15m` |
| `HOLMESGPT_MAX_CONCURRENCY` | `4` |
| `EVENT_QUEUE_SIZE` | `100` |
| `EVENT_DEDUP_TTL` | `10m` |
| `ALERT_SESSION_TTL` | `24h` after the last notification |
| `RESOLVED_SESSION_TTL` | `24h` |
| `ADHOC_SESSION_TTL` | `8h` idle |
| `ALERT_PAYLOAD_MAX_BYTES` | `32768` |
| `RUNBOOK_MAX_BYTES` | `8192` |
| `CONVERSATION_MAX_TURNS` | `6` |
| `CONVERSATION_MAX_BYTES` | `16384` |
| `SLACK_OUTPUT_MAX_CHARS` | `2500` |
| `METRICS_ADDR` | `:9090` |

AlertLens has no model-provider or Holmes API key. Model credentials live only
in HolmesGPT. The MVP relies on cluster networking to protect `/api/chat`.
Enabling HolmesGPT client authentication later requires deliberately adding a
client credential to AlertLens.

The Slack bot user ID is discovered with `auth.test`.

## Alert Identity

The marker carries identity only. It has no status or URL:

```text
<!-- alertlens:alertname={{ .GroupLabels.alertname }},namespace={{ .GroupLabels.namespace }} -->
```

During migration AlertLens also accepts the legacy marker and ignores its
`status` field:

```text
<!-- vigil:alertname={{ .GroupLabels.alertname }},namespace={{ .GroupLabels.namespace }},status={{ .Status }} -->
```

The session key is `am:<alertname>:<namespace>`. Empty namespace is valid for
cluster-scoped alerts. By explicit MVP decision, different Alertmanager groups
with the same alert name and namespace share one thread and RCA session. This
is the known ceiling of the simple marker.

Future PagerDuty sessions use `pd:<incident-id>` and do not use this marker.

## Alertmanager Contract

For every marker, AlertLens calls `GET /api/v2/alerts` with `active=true`,
`silenced=false`, `inhibited=false`, and filters for alert name and namespace.
All matching alerts are retained. Only these fields are passed forward:

- labels and annotations
- startsAt and endsAt
- generatorURL

Duplicate inline `annotations.runbook` values are removed and bounded.

This fetch remains necessary because HolmesGPT 0.35.0 only uses its
Alertmanager source in the CLI `investigate alertmanager` path. `/api/chat`
does not automatically fetch Alertmanager alerts. AlertLens also needs the
query result to distinguish firing from resolved after removing status from
the marker. The Alertmanager URL is not placed in the marker or Holmes prompt.

## HolmesGPT Contract

AlertLens sends `POST /api/chat` with:

- the bounded structured matching alerts
- bounded inline `annotations.runbook`
- original Slack text marked as untrusted advisory input
- bounded previous user and final-assistant turns
- a read-only investigation system instruction
- stable request source, source reference, and conversation ID metadata

HolmesGPT owns investigation data access:

- `kubernetes/logs` for current and previous container stdout
- `victorialogs` for aggregated and historical logs
- `prometheus/metrics` for metrics and alert rules

AlertLens does not duplicate those clients. It reads the response `analysis`,
sanitizes it, bounds it, and posts it to Slack.

The FlowMQ Holmes deployment must separately enable named VictoriaLogs
instances `flowmq` and `management`. This is tracked by
`emqx/flowmq-platform#300`.

## Event Flow

### Receipt

1. Receive a Socket Mode event and ACK immediately.
2. Reject non-monitored channels and self messages.
3. Reject events without a valid marker or explicit mention.
4. Persist the Slack event ID with a ten-minute TTL; ignore duplicates.
5. Add `eyes` and enqueue the work.

The queue is bounded. When full, AlertLens removes `eyes`, adds `x`, and records
a dropped-event metric. The authoritative Alertmanager message remains visible.

### Firing

1. Parse the marker and serialize work by session key.
2. Query Alertmanager.
3. If active alerts exist and no active session exists, create and persist the
   session before the HolmesGPT side effect.
4. Replace `eyes` with `hourglass_flowing_sand` when investigation starts.
5. Post RCA in the parent thread.
6. Replace `hourglass_flowing_sand` with `white_check_mark`.
7. Persist the bounded alert snapshot, thread mapping, and final answer.

An already-active session suppresses repeated RCA and only refreshes activity.
A firing notification for a resolved session reopens it and starts a new RCA.

### Resolved

No matching active alerts plus an active session means resolved. No matches and
no session means stale or unknown and is ignored with a metric. An Alertmanager
query failure never changes state or produces a resolved reaction.

On confirmed resolution AlertLens:

1. Adds `eyes` to the new resolved Slack notification.
2. Posts a short resolved message in the original thread.
3. Removes `eyes` from the resolved notification.
4. Adds `large_green_circle` to the resolved notification and original alert.
5. Keeps the original `white_check_mark`, which means RCA completed.
6. Persists the resolved state.

HolmesGPT is not called for resolution.

### Ad-hoc and Follow-up

- A top-level `@AlertLens` creates an ad-hoc session keyed by the Slack parent
  timestamp and replies in a new thread.
- A mention in a known thread reuses its alert context and bounded conversation.
- A mention in an unknown thread creates an ad-hoc session on that thread.
- Human messages without an explicit mention are ignored.

These requests use the same `eyes` -> `hourglass_flowing_sand` ->
`white_check_mark` or `x` flow.

### Watchdog

`alertname=Watchdog` does not call HolmesGPT or post a thread reply. AlertLens
adds `eyes`, updates the heartbeat metrics, then replaces `eyes` with
`white_check_mark`.

```text
alertlens_watchdog_last_seen_timestamp
alertlens_watchdog_received_total
```

Missing Watchdog detection includes missing metrics:

```promql
absent(alertlens_watchdog_last_seen_timestamp)
or time() - alertlens_watchdog_last_seen_timestamp > 300
```

## Reaction Semantics

| Reaction | Meaning |
|---|---|
| `eyes` | Accepted and queued |
| `hourglass_flowing_sand` | HolmesGPT is running |
| `white_check_mark` | RCA or ad-hoc operation succeeded |
| `large_green_circle` | Alertmanager confirms resolved |
| `x` | The current operation failed |

Reaction failures are non-fatal and counted without message content.

## Session Persistence

Runtime state lives in memory and is recovered from one JSON snapshot on an RWO
PVC. The versioned snapshot contains:

- source-prefixed key, type, state, and timestamps
- Slack channel, parent timestamp, and thread timestamp
- bounded structured alert context
- recent user questions and final HolmesGPT answers
- unexpired Slack event IDs

It contains no credentials, raw Holmes tool output, raw infrastructure logs, or
unbounded prompts.

Writes are serialized to a temporary file, synced, mode `0600`, and atomically
renamed. Startup fails on corrupt state or an unwritable directory. New session
state is persisted before HolmesGPT runs. If Slack has already received a reply
and the subsequent write fails, AlertLens keeps memory state, marks readiness
degraded, and records a persistence error. Expired records are pruned at startup
and periodically.

## Reliability and Limits

- Socket reception and work execution are decoupled by a bounded queue.
- HolmesGPT concurrency is four by default; each session is serialized.
- Alertmanager uses a five-second timeout and at most three bounded retries.
- Slack Web API retry follows `Retry-After`.
- HolmesGPT uses a 15-minute timeout and no automatic retry.
- Users retry explicitly by mentioning AlertLens.
- Alert payload is capped at 32 KiB.
- Inline runbook is capped at 8 KiB.
- Conversation is capped at six turns and 16 KiB.
- Slack output is capped at 2500 characters with a truncation notice.

Oversized alert sets retain identity fields and as many complete alerts as fit;
JSON is never truncated into an invalid document.

Slack text, labels, annotations, and runbooks are untrusted. They are delimited
from system instructions. Output is sanitized for credential patterns and
converted to Slack-compatible markup.

## Health and Metrics

The internal HTTP server exposes:

- `/healthz`: process alive.
- `/readyz`: valid config, readable/writable PVC, connected Socket Mode.
- `/metrics`: Prometheus text exposition.

Metrics cover event outcomes, queue depth, active Holmes calls, Alertmanager and
Holmes latency/outcomes, reaction outcomes, session counts, persistence health,
and Watchdog. Metrics never use alert names, namespaces, thread IDs, event IDs,
or URLs as labels.

## Security

- `automountServiceAccountToken: false`; AlertLens needs no Kubernetes RBAC.
- Non-root process and read-only root filesystem.
- Secrets are never logged, persisted, prompted, or used as metric labels.
- Egress is limited to DNS, Slack HTTPS, Alertmanager, and HolmesGPT.
- Health and metrics use a ClusterIP-only Service.
- HolmesGPT owns all Kubernetes, metrics, logs, and future Git permissions.
- Holmes read-only behavior is enforced by RBAC and toolsets, not prompts.

## Deployment

The image contains a statically built Go binary on a non-root distroless base.
The Helm chart renders one Deployment replica with `strategy: Recreate`, one
RWO PVC, one ClusterIP Service, non-secret configuration, Slack Secret
references, resource limits, and egress NetworkPolicy.

`Recreate` prevents concurrent writers to the RWO snapshot. Short deployment
downtime is acceptable because Alertmanager continues notifying Slack.

## Test Strategy

AlertLens follows the testing trophy: most behavior is covered by integration
tests, supported by focused unit tests and one opt-in real-system E2E.

### Integration Tests

Tests assemble the real router, service, HTTP clients, session manager, reaction
state machine, and snapshot store. Only network transports are replaced:

- `httptest.Server` acts as Alertmanager.
- `httptest.Server` acts as HolmesGPT.
- A fake Slack transport captures reactions and replies.

Integration scenarios cover:

- firing through RCA, reaction transitions, persistence, and reply
- duplicate suppression and resolved-session reopening
- confirmed resolution and stale resolved notifications
- Alertmanager failure without false resolution
- HolmesGPT failure and timeout without automatic retry
- top-level ad-hoc, known-thread, and unknown-thread questions
- unmentioned human conversation being ignored
- Watchdog handling
- queue saturation and per-session ordering
- restart recovery
- snapshot write failure and degraded readiness
- non-fatal reaction failure
- all content limits and sanitizer paths

### Unit Tests

Unit tests are reserved for pure boundary logic:

- new `alertlens:` and legacy `vigil:` marker parsing
- empty namespace and malformed marker handling
- sanitizer behavior
- complete-document truncation
- snapshot encoding, decoding, atomic replacement, and corruption detection
- TTL pruning

### Contract and E2E Tests

Sanitized real Slack and Alertmanager payload fixtures protect external
contracts. One opt-in E2E uses a separate Slack App and test channel:

```text
firing -> RCA -> explicit follow-up -> resolved
```

AlertLens never shares Vigil's app token during parallel testing because Socket
Mode connections would compete for events.

CI runs:

```text
gofmt check
go vet ./...
go test -race -coverprofile=coverage.out ./...
go build ./cmd/alertlens
```

Repository statement coverage begins at a 90% minimum and may only increase.
Critical transitions, persistence, and failure paths are explicitly covered.
Tests are not added solely to execute meaningless lines.

## Migration from Vigil

1. Deploy AlertLens with a separate Slack App and test channel.
2. Validate the full E2E flow without sharing Vigil's app token.
3. Enable the two HolmesGPT VictoriaLogs instances.
4. Stop Vigil before enabling AlertLens on the production alert channel.
5. Enable AlertLens while retaining the compatible `vigil:` marker.
6. Verify firing, follow-up, resolved, Watchdog, and restart recovery.
7. Change the Alertmanager Slack marker from `vigil:` to `alertlens:`.
8. Keep legacy parsing for one release, then remove it after metrics show no
   legacy events.

## Future PagerDuty Compatibility

PagerDuty does not reuse the Slack marker. A future V3 webhook adapter verifies
the PagerDuty signature, maps `event.data.id` to `pd:<incident-id>`, derives
state from the webhook event type, and feeds the existing session and HolmesGPT
flow. No PagerDuty code or generic source factory is present in the MVP.

## Acceptance Criteria

### Firing Alert

```gherkin
Given Alertmanager posts a marked alert to a configured Slack channel
When AlertLens confirms matching active alerts through Alertmanager
Then it persists the session before invoking HolmesGPT
And transitions the parent reaction from eyes to hourglass_flowing_sand
And posts a bounded RCA in the parent thread
And replaces hourglass_flowing_sand with white_check_mark
```

### Duplicate Alert

```gherkin
Given an active session already has a completed RCA
When Alertmanager repeats the same alert notification
Then AlertLens refreshes the session
And does not invoke HolmesGPT again
```

### Resolved Alert

```gherkin
Given an active alert session exists
When a marked notification arrives and Alertmanager has no matching active alerts
Then AlertLens posts a resolved update in the original thread
And adds large_green_circle to the original and resolved parent messages
And does not invoke HolmesGPT
```

### Ad-hoc Follow-up

```gherkin
Given an AlertLens session exists for a Slack thread
When a user explicitly mentions AlertLens in that thread
Then AlertLens sends bounded alert context and recent conversation to HolmesGPT
And posts the answer in the same thread
```

### Restart Recovery

```gherkin
Given sessions have been persisted to the PVC
When the single AlertLens pod restarts
Then it restores thread mappings, bounded conversation, and unexpired dedup state
And continues follow-ups in the original threads
```

### AlertLens Failure

```gherkin
Given AlertLens is unavailable
When Alertmanager sends an alert
Then Alertmanager still posts the authoritative alert directly to Slack
```
