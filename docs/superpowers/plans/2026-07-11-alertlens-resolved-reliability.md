# AlertLens Resolved and Reliability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the alert lifecycle and its operational safety with confirmed resolution, restart-safe event deduplication, per-session ordering, Prometheus metrics, and Slack rate-limit handling.

**Architecture:** Extend the existing service rather than adding source/provider layers. Session state remains the source of thread continuity; a fixed set of keyed lock shards serializes one alert identity while retaining cross-alert Holmes concurrency. A private Prometheus registry is passed concretely to the service and exposed on the existing internal HTTP server.

**Tech Stack:** Go 1.25, standard library, `prometheus/client_golang` v1.23.2, existing `slack-go/slack` v0.27.0.

## Global Constraints

- Resolution is inferred only from a successful Alertmanager query returning zero matches; marker status is never used.
- No match plus no active session is stale and produces no resolved message or green reaction.
- Confirmed resolution never calls HolmesGPT.
- Resolution replies in the original thread and adds `large_green_circle` to both the original and new resolved parent messages while retaining the original `white_check_mark`.
- A firing notification for a resolved session reopens it and performs a new RCA.
- Slack event IDs are persisted before queueing for ten minutes by default and survive restart.
- Work sharing a session key is serialized; unrelated alerts can still use all configured Holmes workers.
- Metrics never label on alert name, namespace, channel, thread, event ID, or URL.
- Slack 429 retry obeys `Retry-After`, is context-cancellable, and is attempted once; other errors are not retried.
- No ad-hoc/app-mention/follow-up behavior is introduced in this milestone.
- Every commit follows Conventional Commits 1.0.0 and repository coverage remains at least 90%.

---

### Task 1: Confirmed resolution and reopening

**Files:**
- Modify: `internal/service/service.go`
- Modify: `internal/service/integration_test.go`

**Interfaces:**
- Adds `ResolvedSessionTTL time.Duration` to `service.Config`.
- Uses the existing session states `active` and `resolved`; no status enum package is added.

- [x] **Step 1: Write a failing confirmed-resolution integration test**

Start from an active persisted session whose original parent is `C1/100.1`. Submit a new marked event at `C1/200.1` while the Alertmanager fake returns no matches. Assert the exact additional operations:

```text
add:eyes:C1:200.1
reply:C1:100.1:🟢 Alertmanager confirms this alert is resolved.
remove:eyes:C1:200.1
add:large_green_circle:C1:200.1
add:large_green_circle:C1:100.1
```

Assert Holmes is never called and the record is persisted as `resolved` with `ExpiresAt=now+ResolvedSessionTTL`.

- [x] **Step 2: Verify RED and implement resolution**

Run: `go test ./internal/service -run TestConfirmedResolution -v`

Expected: FAIL because the current zero-match path only removes `eyes`.

On zero matches, read the current session. If it is not active, remove `eyes` and return. If active, post the fixed resolved sentence to `record.ThreadTS` (falling back to `record.ParentTS`), perform the reactions above, then persist resolved state and timestamps. A query error continues to end in `x` without changing state.

- [x] **Step 3: Add stale, duplicate resolved, and reopen tests**

Cover:

- no session + no matches: remove `eyes`, no reply or green reaction;
- already-resolved session + no matches: no duplicate reply;
- resolved session + active matches: replace it with a new active claim, call Holmes once, and update the original thread anchor to the new firing parent.

- [x] **Step 4: Verify and commit**

Run: `gofmt -w internal/service && go test -race ./internal/service -count=10 && go test ./...`

Expected: PASS.

```bash
git add internal/service
git commit -m "feat(service): handle confirmed alert resolution"
```

---

### Task 2: Restart-safe event deduplication and per-session ordering

**Files:**
- Modify: `internal/service/service.go`
- Modify: `internal/service/integration_test.go`
- Modify: `internal/session/store.go`
- Modify: `internal/session/store_test.go`

**Interfaces:**
- Adds `EventDedupTTL time.Duration` to `service.Config`.
- Produces `(*session.Store).Prune(now time.Time) error` using the existing private prune function and atomic update.
- Uses 64 fixed `sync.Mutex` shards selected by FNV-1a of the session key.

- [ ] **Step 1: Write failing event-ID dedup tests**

Assert that two submissions with `ID=Ev1` produce only one `eyes`, one Alertmanager call, and one RCA. Reopen the same state file in a new store/service instance and assert `Ev1` remains ignored until expiry. Empty event IDs retain the existing test-friendly behavior and are not deduplicated.

- [ ] **Step 2: Implement atomic receipt dedup**

Before adding `eyes`, `Submit` calls `Store.Update` and atomically checks `Snapshot.EventIDs[event.ID]`. An unexpired ID returns false without reactions. A new ID stores `now+EventDedupTTL`; persistence failure returns false and leaves readiness degraded. Do not add an in-memory duplicate map beside the snapshot.

- [ ] **Step 3: Write and implement prune tests**

Test `Store.Prune` removes only expired sessions/event IDs and persists the result. In `Service.Run`, one fixed one-minute ticker calls `Prune(now)` until cancellation; record persistence failures in metrics in Task 3. Do not add a cleanup interval configuration knob.

- [ ] **Step 4: Write a failing same-session ordering test**

With two workers, block the first Holmes call, submit a duplicate identity, and assert the second Alertmanager call does not begin until the first work item finishes. Submit a different identity and assert it can reach Holmes while the first remains blocked.

- [ ] **Step 5: Implement bounded lock sharding**

Add `[64]sync.Mutex` to `Service`. At the start of `handle`, hash `identity.Key()` with `hash/fnv`, lock its shard, and defer unlock. This deliberately permits unrelated keys that collide to serialize without leaking one mutex per alert.

- [ ] **Step 6: Verify and commit**

Run: `gofmt -w internal/service internal/session && go test -race ./internal/service ./internal/session -count=10 && go test ./...`

Expected: PASS with no race report.

```bash
git add internal/service internal/session
git commit -m "feat(service): deduplicate and order alert events"
```

---

### Task 3: Operational metrics and `/metrics`

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/observability/metrics.go`
- Create: `internal/observability/metrics_test.go`
- Modify: `internal/service/service.go`
- Modify: `internal/service/integration_test.go`
- Modify: `internal/health/handler.go`
- Modify: `internal/health/handler_test.go`
- Modify: `cmd/alertlens/main.go`
- Modify: `cmd/alertlens/main_test.go`

**Interfaces:**
- Produces `observability.New() *Metrics` with a private `prometheus.Registry`.
- Produces `(*Metrics).Handler() http.Handler` and concrete recording methods used by the service.
- Changes `health.New(readiness, metricsHandler)` to expose `/healthz`, `/readyz`, and `/metrics` from one mux.

- [ ] **Step 1: Add Prometheus client and failing registry tests**

Run: `go get github.com/prometheus/client_golang@v1.23.2`.

Test a private registry exposes these names and only bounded labels:

```text
alertlens_events_total{outcome}
alertlens_reactions_total{operation,outcome}
alertlens_alertmanager_requests_total{outcome}
alertlens_holmes_requests_total{outcome}
alertlens_persistence_errors_total
alertlens_queue_depth
alertlens_holmes_active
alertlens_sessions
alertlens_watchdog_last_seen_timestamp
alertlens_watchdog_received_total
```

Also expose Alertmanager/Holmes duration histograms without dynamic labels. Assert gathered label names never contain `alertname`, `namespace`, `channel`, `thread`, `event`, or `url`.

- [ ] **Step 2: Implement concrete metrics and service recording**

Use `prometheus.NewRegistry`, standard collectors, `CounterVec` only for the bounded labels above, and `promhttp.HandlerFor`. Record accepted/duplicate/dropped/stale/firing/resolved/watchdog/failed outcomes, reaction success/failure, client outcomes/durations, active Holmes calls, queue depth, session count, and persistence failures. Watchdog sets Unix seconds and increments its counter.

- [ ] **Step 3: Add `/metrics` handler tests and wire startup**

Test the handler returns Prometheus text and readiness remains generic. Main creates one `Metrics`, passes it to the service, and exposes its handler. Update Helm probes/service only if rendering changes are required; the existing port 9090 remains.

- [ ] **Step 4: Verify and commit**

Run: `go mod tidy && gofmt -w cmd internal && go test -race -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1`

Expected: PASS and total coverage at least 90%.

```bash
git add go.mod go.sum cmd internal
git commit -m "feat(metrics): expose alert lifecycle metrics"
```

---

### Task 4: Slack `Retry-After` and milestone gate

**Files:**
- Modify: `internal/slack/client.go`
- Modify: `internal/slack/client_test.go`
- Modify: `README.md`
- Modify: `docs/superpowers/plans/2026-07-11-alertlens-resolved-reliability.md`

**Interfaces:**
- Adds one private `retryRateLimit(ctx, operation)` helper; public Slack/service interfaces stay unchanged.

- [ ] **Step 1: Write failing Slack 429 tests**

For add/remove reaction and reply, return a `slack.RateLimitedError{RetryAfter: 10ms}` once and success next. Assert two attempts. Add cancellation and ordinary-error cases asserting no second call.

- [ ] **Step 2: Implement one context-aware retry**

Call the operation, use `errors.As` for `*slack.RateLimitedError`, wait exactly `RetryAfter` with a timer/select on context, then call once more. Preserve `already_reacted` and `no_reaction` handling after either attempt.

- [ ] **Step 3: Run the full milestone verification**

Run the CI-equivalent gate: gofmt, vet, race+coverage >=90%, Go build, Helm lint/unit, actionlint, Docker build, then render and server-side dry-run using `~/.kube/flowmq-dev-tiger.yaml`.

- [ ] **Step 4: Update README and commit**

Document resolved behavior, event-ID restart dedup, `/metrics`, and the Watchdog missing expression. Do not claim ad-hoc/follow-up support.

```bash
git add internal/slack README.md docs/superpowers/plans/2026-07-11-alertlens-resolved-reliability.md
git commit -m "fix(slack): honor rate limit retry timing"
```

---

## Milestone 3A Completion Check

- Confirmed resolution updates the original thread without HolmesGPT.
- Reopened alerts run a new RCA; stale/duplicate resolved notifications stay quiet.
- Event-ID dedup survives restart and expires.
- Same-session work is ordered while unrelated alerts retain concurrency.
- `/metrics` exposes bounded-cardinality lifecycle, dependency, persistence, session, queue, and Watchdog signals.
- Slack operations honor one `Retry-After` retry.
- Full CI-equivalent and Kubernetes server-side dry-run gates pass.
- Ad-hoc and thread follow-up remain isolated to milestone 3B.
