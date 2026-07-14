# AlertLens Design

Date: 2026-07-11
Status: Approved, updated 2026-07-13

## Purpose

AlertLens is an Alertmanager-first RCA companion for Slack. Alertmanager keeps
the authoritative notification path; AlertLens adds read-only HolmesGPT
investigation without becoming part of alert delivery.

```text
Alertmanager -> Slack       authoritative notification and trigger
Slack -> AlertLens          event, thread anchor, and explicit questions
AlertLens -> Alertmanager   required active-alert verification for firing
AlertLens -> HolmesGPT      read-only investigation
AlertLens -> Slack          RCA, answers, failures, and reactions
```

If AlertLens is unavailable, Alertmanager still notifies Slack.

## Scope

The MVP includes:

- Slack Socket Mode ingestion in the configured `SLACK_ALERT_CHANNEL`.
- Automatic investigation for every valid firing notification.
- A reaction-only resolved path.
- Explicit top-level and thread `@AlertLens` questions.
- Slack-derived conversation context for thread questions.
- Active Alertmanager verification, a bounded Verified Alert Snapshot, and
  inline `annotations.runbook` context for automatic investigation.
- Bounded prompts and replies, credential sanitization, health, readiness, and
  operational metrics.

The MVP excludes persistent sessions, event receipts, lifecycle records,
cooldowns, snapshots, thread mappings, active-active guarantees, remediation,
GitOps, PR creation, arbitrary shell execution, and Alertmanager webhook
ingestion.

## Slack Boundary

AlertLens only observes the channel configured in `SLACK_ALERT_CHANNEL`.
Alertmanager bot messages are candidates for automatic handling. Human
messages are handled only when Slack emits an explicit app mention event.
Ordinary thread discussion is ignored.

Alertmanager notifications carry a hidden marker:

```html
<!-- alertlens:alertname=HighCPU,namespace=prod,status=firing -->
<!-- alertlens:alertname=ClusterDown,namespace=,status=resolved -->
```

The legacy `vigil:` prefix remains accepted during migration, but it has the
same required fields and behavior. Missing `alertname`, missing `namespace`, or
an unknown/missing status produces `x` and no Alertmanager or Holmes request.
An empty namespace is valid and represents a cluster-scoped alert.

## Identity and Notification Groups

Alert Identity is the binary key `alertname + namespace`. Status is not part of
identity. No placeholder such as `global` is introduced for an empty
namespace.

An Alertmanager Notification Group is a delivery grouping, not an Alert
Identity. Several active groups can share one identity. For Active Alert
Verification, AlertLens queries the current active alerts and requires at least
one instance whose `alertname` and `namespace` match. Silenced and inhibited
matches still count as active. The Verified Alert Snapshot includes every match
that fits the payload budget and may combine instances from several notification
groups; it always preserves `verified`, Alert Identity, and `truncated`.
`group_by` fields are intentionally absent from the marker and query selector;
the Slack root still shows which group triggered the current investigation.

## Automatic Investigation

Every notification with `status=firing` runs this flow:

1. Add `eyes` to the notification.
2. Serialize work for that Slack thread in-process.
3. Query Alertmanager for all active instances matching Alert Identity.
4. If the query fails, reply with its actual sanitized, bounded reason, replace
   `eyes` with `x`, and stop before Holmes.
5. If the query succeeds with zero matches, reply with an explicit no-match
   reason, record `no_match`, replace `eyes` with `x`, and stop without retry.
6. Build a bounded Holmes request from the notification root, Verified Alert
   Snapshot, and inline runbooks. State that verification succeeded immediately
   before the request and the snapshot may be truncated.
7. Replace `eyes` with `hourglass_flowing_sand` while Holmes runs.
8. Post the bounded sanitized RCA in the notification thread.
9. Replace the hourglass with `white_check_mark`, or with `x` on failure.

No cooldown or stored episode suppresses repeated firing notifications. A rare
Slack redelivery can therefore repeat work and replies. Automatic investigation
does not read Slack thread history. Verification is point-in-time; a later
resolution does not cancel an RCA already in progress.

The Holmes metadata is:

```text
request_source: alert_investigation
source_ref: am:<alertname>:<namespace>
conversation_id: slack:<channel_id>:<notification_ts>
```

## Resolved Notifications

A valid notification with `status=resolved` only replaces `eyes` with
`large_green_circle` on that notification. It does not query Alertmanager, call
Holmes, reply in an older thread, or update an older message.

## Ask and Conversation Context

Every explicit `@AlertLens` is an Ask, even if its text contains a marker. Ask
never queries Alertmanager. HolmesGPT may use its configured tools to inspect
current state.

For a top-level Ask, AlertLens sends the current question and replies in the
new thread. For a thread Ask, AlertLens reads all pages of
`conversations.replies` up to the current message, then retains only:

1. the thread root;
2. prior human messages explicitly mentioning AlertLens; and
3. prior successful AlertLens answers.

The current question is sent separately and is not duplicated in history.
Other discussion is excluded. The root is retained and the newest eligible
messages are added within the byte budget. There is no page-count or turn-count
limit. If any Slack history page fails, the Ask ends with `x` and Holmes is not
called.

Ask metadata uses the Slack thread for both source and conversation identity:

```text
request_source: freeform
source_ref: slack:<channel_id>:<root_ts>
conversation_id: slack:<channel_id>:<root_ts>
```

The conversation ID is metadata only. AlertLens always reconstructs the
history from Slack.

## Failure Replies and Reactions

Active Alert Verification and Holmes failures are replied to in the relevant
thread after credential sanitization and Slack output bounding. A query error
uses its actual reason; a successful query with zero matches uses an explicit
no-match reason. Both verification failures stop before Holmes and produce `x`;
Holmes failure produces `x` for both Ask and automatic investigation.

| Reaction | Meaning |
| --- | --- |
| `eyes` | accepted or queued |
| `hourglass_flowing_sand` | Holmes is running |
| `white_check_mark` | RCA or Ask completed |
| `large_green_circle` | this notification is resolved |
| `x` | validation or operation failure |

Reaction API failures are counted but do not stop useful work.

## Watchdog

Watchdog has no special branch. If a Watchdog firing notification reaches a
monitored channel, it runs ordinary automatic investigation. AlertLens exposes
no Watchdog heartbeat metrics; an end-to-end dead man's switch must be observed
by an independent monitoring path.

## Stateless Processing and Concurrency

AlertLens reconstructs work from the current Slack event, Slack thread history
for Ask, and current Alertmanager data for firing. It stores no session,
conversation, lifecycle, alert snapshot, thread mapping, state file, or event
receipt.

A bounded in-memory queue decouples Socket Mode reception from work. Holmes
concurrency defaults to four. A fixed set of in-process locks serializes work
for the same Slack thread while allowing unrelated threads to proceed. These
are transient coordination, not memory. Accepted work drains during graceful
shutdown for up to 25 seconds.

## Limits and Security

- Alertmanager timeout: 5 seconds with at most three bounded retries.
- Holmes timeout: 15 minutes without automatic retry.
- Alert payload: 32 KiB, with a configurable minimum of 128 bytes. If the
  verified identity cannot fit, the operation fails before Holmes rather than
  dropping verification fields.
- Inline runbooks: 8 KiB.
- Conversation context: 256 KiB, with no turn limit.
- Slack output: 2500 characters with a truncation notice.

Slack text, Alertmanager content, runbooks, and Holmes output are untrusted.
They are structurally delimited and sanitized for credential patterns.
AlertLens never mutates infrastructure. Kubernetes access and other
investigation tools belong to HolmesGPT and must be read-only by RBAC/tool
configuration.

The pod runs non-root with a read-only filesystem and no service-account token.
The chart creates no PVC. NetworkPolicy allows configured Alertmanager and
Holmes destinations plus Slack HTTPS and DNS.

## Health and Metrics

The internal HTTP server exposes:

- `/healthz`: process alive;
- `/readyz`: Slack Socket Mode connected;
- `/metrics`: Prometheus exposition.

Metrics cover bounded event outcomes, queue depth, active Holmes calls,
Alertmanager (`success`, `error`, `no_match`) and Holmes latency/outcomes, and
reaction outcomes. They do not include alert names, namespaces, channel IDs,
thread IDs, event IDs, or URLs as labels.

## Test Strategy

The repository follows the testing trophy and integration-first TDD:

- Service integration tests cover firing, resolved, Ask, active-alert
  verification, real sanitized errors, reactions, queueing, shutdown, and
  thread serialization.
- HTTP client tests cover Alertmanager and Holmes protocols.
- Slack contract tests cover event translation, full cursor pagination,
  root/question/answer filtering, and Web API behavior.
- Focused unit tests cover marker parsing, prompt bounds, sanitization, and
  conversation byte selection.
- Helm tests prove a hardened stateless deployment.
- The opt-in real E2E covers firing RCA, a manual Ask, and a resolved
  notification receiving `large_green_circle`.

## Acceptance Criteria

```gherkin
Given a valid firing notification in a monitored channel
And Alertmanager has a matching active alert
When AlertLens receives it
Then AlertLens runs Holmes once for that notification
And it posts either the RCA or a sanitized Holmes failure in that thread
```

```gherkin
Given a valid firing notification in a monitored channel
And Alertmanager cannot be queried or has no matching active alert
When AlertLens receives it
Then AlertLens posts the distinct bounded verification failure
And marks the notification failed
And does not call Holmes
```

```gherkin
Given a valid resolved notification
When AlertLens receives it
Then it adds large_green_circle to that notification
And makes no Alertmanager or Holmes request
```

```gherkin
Given an explicit thread mention
When Slack thread history is readable
Then AlertLens sends the root, prior explicit questions, and prior successful AlertLens answers to Holmes
And sends the current question separately
And posts the answer in the same thread
```

```gherkin
Given AlertLens is unavailable
When Alertmanager sends an alert
Then Alertmanager still posts the authoritative notification directly to Slack
```
