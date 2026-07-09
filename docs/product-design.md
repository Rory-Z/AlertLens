# AlertLens Product Design

Date: 2026-07-08

## Summary

AlertLens is a small no-UI service for Alertmanager-driven RCA in Slack.

It should not become another broad SRE agent. The core product is:

```text
Alertmanager -> Slack       # existing authoritative alert delivery and trigger
Slack Events -> AlertLens   # RCA trigger, thread anchor, and status UX
AlertLens    -> Slack       # RCA and follow-ups inside the same threads
```

If AlertLens is down, alerts still reach Slack through the mature Alertmanager integration. The worst case is losing RCA, not losing alert delivery.

## Product Boundary

AlertLens keeps:

- Slack Events listener for Alertmanager bot messages, status reactions, and `@AlertLens` mentions.
- Slack Web API replies into threads and reactions on parent messages.
- HolmesGPT RCA calls.
- Lightweight in-process/session state for deduplication and conversation context.
- Deterministic enrichment before RCA.
- Watchdog heartbeat metrics.
- Optional declarative scheduled checks.

AlertLens does not include:

- A web UI.
- GitOps remediation.
- PR creation.
- ArgoCD-specific notification handling.
- A general autonomous agent.
- Arbitrary shell command execution.
- Slack commands that create or mutate scheduled jobs at runtime.
- Alertmanager webhook ingestion in the MVP.
- External thread storage or active-active HA in the MVP.

## Why Not Replace Alertmanager -> Slack

Alertmanager's direct Slack integration is the reliable notification path. Replacing it with AlertLens would make AlertLens part of the critical alert delivery chain:

```text
Alertmanager -> AlertLens -> Slack
```

That is not acceptable for the first version. If AlertLens fails, operators must still see the alert.

The preferred shape is non-replacing enhancement:

```text
Alertmanager -> Slack -> AlertLens
```

AlertLens can fail independently after Slack receives the alert. Alertmanager remains the source of notification truth.

## Slack-Triggered RCA

AlertLens should use the Alertmanager Slack message as the single event source for the MVP. This keeps the UX and control flow close to Vigil while removing Vigil's agent, GitOps, ArgoCD, and arbitrary shell surface area.

Recommended flow:

1. Alertmanager posts a short alert message to Slack.
2. AlertLens receives the Slack bot message through Slack Events.
3. AlertLens extracts a stable fingerprint or group key from the message marker.
4. AlertLens adds a status reaction to the parent message.
5. AlertLens enriches the alert by querying Alertmanager APIs, runbooks, and configured read-only sources.
6. AlertLens asks HolmesGPT for RCA.
7. AlertLens posts the bounded RCA into the same Slack thread.

Because the Slack event includes `channel_id` and `message_ts`, M1 does not need an external thread mapping database. The parent Slack message is the session anchor for the current handling flow.

Small in-memory or local persisted state is still useful for:

- in-flight deduplication,
- short conversation context,
- rate limits,
- alert storm control.

But the MVP should not introduce Redis/Postgres solely for thread matching. Add external state only when active-active HA, durable incident history, or cross-restart recovery-thread continuity becomes a real requirement.

## Slack Status UX

AlertLens should preserve Vigil's Slack thread experience. Operators should be able to look at the original Alertmanager Slack message and see whether AlertLens noticed it, is working on it, or failed.

Slack Events are the RCA trigger in the MVP. They also provide the message identity needed for reactions and thread replies.

Parent message reactions:

| Reaction | Meaning |
|----------|---------|
| `eyes` | AlertLens saw the Alertmanager Slack message and queued or correlated the alert |
| `hourglass_flowing_sand` | RCA is running, optional if `eyes` is not enough |
| `white_check_mark` | RCA or resolved handling completed |
| `x` | RCA failed or AlertLens could not complete handling |

Recommended flow:

1. Slack Events receives the Alertmanager parent message.
2. AlertLens extracts the alert key from the marker.
3. AlertLens adds `eyes` to the parent message.
4. AlertLens gathers context and runs RCA.
5. On success, AlertLens removes `eyes` and adds `white_check_mark`.
6. On failure, AlertLens removes `eyes` and adds `x`, then posts a short thread error only if configured.
7. On resolved, AlertLens posts a short resolved summary in the original thread and keeps or adds `white_check_mark`.

Reaction failures should not fail alert handling. They should be logged and counted as metrics because Slack permissions, deleted messages, or rate limits can make reactions unavailable.

## High Availability

M1 should not promise active-active HA. Slack-triggered AlertLens should run as a single active consumer, matching Vigil's practical deployment model but with much less security-sensitive behavior.

The important reliability boundary is that Alertmanager still posts alerts directly to Slack. If AlertLens is down, operators still see alerts.

External state and active-active processing should be deferred until there is evidence they are needed.

If future active-active HA is required, add:

- external state for session/thread/dedup records,
- idempotency keys based on Slack event ID and alert key,
- duplicate reaction/reply suppression,
- optional Alertmanager webhook ingestion.

Minimum future state:

```text
groupKey/fingerprint -> Slack channel + thread_ts + last status + last sent time
```

Recommended stores:

- Redis for simple TTL-backed mappings.
- Postgres if the project later needs durable incident history and querying.

Without an external store, AlertLens cannot guarantee that repeated or resolved top-level Alertmanager messages return to the original Slack thread after restart or across active replicas. That is acceptable for the MVP; Alertmanager's direct Slack message remains visible even if AlertLens loses continuity.

## Slack Noise Policy

The Slack channel should stay readable.

Channel parent message:

```text
[critical] PodCrashLooping prod/api - RCA running in thread
```

Thread replies:

- Compact alert facts.
- HolmesGPT RCA.
- Follow-up answers.
- Resolved summary.

Hard limits:

- RCA output should be bounded, for example 1500-2500 characters.
- Long results should become a concise summary with truncation noted.
- Automatic RCA should be controlled by labels such as severity, team, namespace, or route.
- Warning-level alerts can be thread-only, rate-limited, or ignored depending on policy.

## HolmesGPT Policy

HolmesGPT is the RCA engine, not an autonomous remediation agent.

AlertLens should:

- Send structured alert payloads and bounded enrichment context.
- Use timeouts and rate limits.
- Sanitize all output before Slack.
- Treat HolmesGPT output as advisory analysis.
- Never create PRs, commits, silences, or cluster mutations in the MVP.

## Deterministic Enrichment

Before calling HolmesGPT, AlertLens should attach cheap deterministic context when available:

- Alert labels and annotations.
- Alertmanager group key and status.
- Runbook URL content or summary.
- Recent matching alert history.
- Prometheus trend context.
- Kubernetes workload evidence, only if a safe read-only collector exists.

This is useful only when bounded. The project should prefer small fixed context over broad collection.

## Private Runbook Fetching

AlertLens may need to fetch private runbooks from GitHub before asking HolmesGPT.

Unlike Vigil, AlertLens does not need a token sidecar in the MVP. Vigil isolated GitHub App credentials because its main process included an agent/tool-execution path. AlertLens should not have that path: no arbitrary shell, no autonomous remediation agent, and no user-defined code execution. With that boundary, the main process can safely mint short-lived GitHub App installation tokens from a Kubernetes Secret.

Recommended flow:

```text
AlertLens main process
  -> load GitHub App private key from Kubernetes Secret
  -> mint short-lived installation token
  -> fetch allowlisted repo/path runbook
  -> pass bounded sanitized runbook excerpt to HolmesGPT
```

Rules:

- Only fetch from configured owners/repos.
- Only fetch allowed path prefixes, for example `runbooks/` or `docs/runbooks/`.
- Never log tokens or include them in metrics, prompts, or Slack output.
- Cache installation tokens until shortly before expiry.
- Cap runbook content length before adding it to HolmesGPT context.
- Runbook fetch failures should degrade RCA quality, not block alert handling.

Add a sidecar only if AlertLens later gains untrusted execution in the main process, such as arbitrary shell, user plugins, or an agent that can choose its own repo/path reads.

## Watchdog

Alertmanager Watchdog should be treated as pipeline health, not an incident.

Behavior:

```text
alertname=Watchdog -> record heartbeat metric only
```

AlertLens should not call HolmesGPT for Watchdog alerts and should not post recurring Watchdog RCA messages to Slack.

Metrics:

```text
alertlens_watchdog_last_seen_timestamp
alertlens_watchdog_received_total
```

Missing Watchdog should be alerted by a path that does not depend on AlertLens:

```promql
time() - alertlens_watchdog_last_seen_timestamp > 300
```

## Ad-hoc Slack Use

AlertLens should support explicit mentions without becoming Vigil again.

Supported:

```text
channel @AlertLens what is wrong with prod/api?
-> create a thread and ask HolmesGPT

thread @AlertLens investigate this
-> use existing thread alert/RCA context and ask HolmesGPT
```

Rules:

- Only respond in configured Slack channels.
- Require explicit `@AlertLens`.
- Top-level mentions create a thread.
- Thread mentions reuse existing alert context when present.
- No GitOps.
- No arbitrary shell.
- No cron management commands.

## Scheduled Checks

Scheduled checks are allowed, but only as declarative HolmesGPT prompts.

Example:

```yaml
scheduledChecks:
  - id: daily-cluster-summary
    schedule: "0 9 * * 1-5"
    channel: "#sre"
    prompt: "Summarize current cluster health and recent critical alerts."
```

Rules:

- Config file or Helm values only.
- Default off.
- Timeout, rate limit, and output length cap per job.
- Failures emit metrics and a short Slack error at most.
- No Slack commands to create/delete jobs.
- No arbitrary script actions.

## MVP Behaviors

### Alertmanager RCA

```gherkin
Given Alertmanager sends a firing alert to Slack
When AlertLens receives the Slack event for the Alertmanager message
Then AlertLens asks HolmesGPT for RCA
And posts a bounded sanitized RCA into the existing Slack thread
```

### Slack Status Reactions

```gherkin
Given Alertmanager posts a firing alert message to Slack
When AlertLens receives the Slack event for that message
Then AlertLens records the message channel and timestamp
And adds an eyes reaction to the parent message
```

```gherkin
Given AlertLens has posted RCA in the alert thread
When RCA handling completes successfully
Then AlertLens removes the eyes reaction when present
And adds a white_check_mark reaction to the parent message
```

### AlertLens Failure

```gherkin
Given AlertLens is down
When Alertmanager sends a firing alert
Then Alertmanager still posts the alert directly to Slack
And operators still see the alert
```

### Watchdog

```gherkin
Given Alertmanager sends a Watchdog alert to Slack
When AlertLens receives the Slack event for that Watchdog message
Then AlertLens updates heartbeat metrics
And does not call HolmesGPT
And does not post recurring Slack RCA
```

### Ad-hoc Thread Follow-up

```gherkin
Given a Slack thread has an AlertLens alert context
When a user posts "@AlertLens investigate this"
Then AlertLens sends the thread context to HolmesGPT
And posts the answer in the same thread
```

## Milestones

### M0: Skeleton

- HTTP health endpoint.
- Metrics endpoint.
- Config loading.
- Slack client.
- HolmesGPT client.

### M1: Slack-Triggered Alertmanager RCA

- Slack Events listener for Alertmanager parent messages and `@AlertLens` mentions.
- Vigil-style parent message status reactions.
- Alertmanager API lookup for structured alert/rule context from marker data.
- HolmesGPT RCA reply into Slack thread.
- Output sanitization and length cap.

### M2: Reliability

- Watchdog heartbeat metrics.
- Retry and rate limiting.
- Alert storm control.
- Resolved handling.

### M3: Optional Webhook and Active-Active

- Alertmanager webhook receiver if Slack-triggering becomes insufficient.
- External store for thread/session/dedup state.
- Active-active deployment.

### M4: Operator Convenience

- `@AlertLens` ad-hoc questions.
- Declarative scheduled checks.
- Runbook enrichment.

## Name

AlertLens fits the narrowed product: it gives a clearer RCA view into alerts. It does not imply autonomous remediation or a broad SRE agent.
