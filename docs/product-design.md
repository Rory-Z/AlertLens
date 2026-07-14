# AlertLens Product Design

Date: 2026-07-08
Updated: 2026-07-13

## Summary

AlertLens is a small, stateless, no-UI companion that adds HolmesGPT
investigation to Alertmanager notifications already delivered to Slack.

```text
Alertmanager -> Slack       authoritative alert delivery
Slack Events -> AlertLens   investigation trigger and thread UX
AlertLens -> Slack          RCA, Ask answers, failures, and reactions
```

If AlertLens fails, operators still receive the alert. The product does not
replace Alertmanager's Slack integration and does not remediate infrastructure.

## Product Boundary

AlertLens keeps:

- Slack Socket Mode for Alertmanager bot messages and explicit app mentions;
- thread replies and message reactions;
- required active-alert verification for firing notifications;
- bounded read-only HolmesGPT requests;
- Slack-derived context for explicit thread questions;
- health, readiness, and low-cardinality operational metrics.

AlertLens does not keep:

- sessions, thread mappings, lifecycle records, snapshots, event receipts, or
  conversation files;
- cooldowns or alert-episode suppression;
- Watchdog-specific metrics;
- a web UI, GitOps, PR creation, scheduled mutable jobs, or arbitrary shell;
- Alertmanager webhook ingestion or active-active delivery guarantees.

## Why Slack Remains the Source of Conversation

Slack already owns the user-visible root, questions, and AlertLens answers.
Persisting a second conversation copy would introduce edit, deletion,
retention, and synchronization semantics that the product does not need.
AlertLens accepts that content deleted or expired from Slack can no longer be
continued.

For each explicit thread Ask, AlertLens reads the entire available thread and
filters it to the root, prior explicit `@AlertLens` questions, and AlertLens
answers. It keeps the root and newest eligible messages within 256 KiB. The
current Ask is separate. Ordinary discussion is not sent to Holmes.

## Alert Semantics

An Alert Identity is `alertname + namespace`; the marker always carries both
fields, while an empty namespace value means a cluster-scoped alert.
Alertmanager notification grouping is a separate
delivery concern. Multiple groups can share one identity.

The hidden Slack marker carries identity plus the status of that notification
group:

```html
<!-- alertlens:alertname=HighCPU,namespace=prod,status=firing -->
```

Every firing notification first performs Active Alert Verification. AlertLens
queries all current active instances matching the binary identity; silenced and
inhibited matches still count as active. A successful point-in-time verification
starts RCA with a bounded Verified Alert Snapshot that may include instances
from several Notification Groups. The snapshot always preserves `verified`,
Alert Identity, and whether instance details were truncated. It does not put
`group_by` fields into the marker or selector. The Slack notification itself
identifies the group that triggered the current RCA.

If Alertmanager cannot be queried, AlertLens posts the actual sanitized error
reason, marks the operation failed, and does not call Holmes. A successful query
with zero matches produces a distinct no-match failure with the same outcome and
is not retried. A later resolution does not cancel an RCA that already passed
verification. If the verified identity itself cannot fit the alert payload
budget, the operation also fails before Holmes rather than dropping verification
fields.

A resolved notification only receives `large_green_circle`. It does not update
an older firing thread or call Alertmanager/Holmes.

## Ask Semantics

Only explicit `@AlertLens` messages in the Monitored Channel are handled. Every
Ask follows the same path, regardless of whether the thread root is an alert or
ordinary Slack message:

1. reconstruct eligible Slack context;
2. send the current question separately to Holmes;
3. post the answer in the same thread.

Ask never queries Alertmanager. Holmes may query current systems through its
own configured read-only tools. If Slack history cannot be read, AlertLens
marks the Ask failed without calling Holmes.

Automatic Investigation and Ask use the same configured Holmes Response
Language. Its default, `auto`, leaves language selection to Holmes; any other
value adds a system-level response-language directive. AlertLens warnings and
failure replies remain unchanged.

## Noise and Failure Policy

The reaction sequence makes state visible without extra channel messages:

| Reaction | Meaning |
| --- | --- |
| `eyes` | accepted |
| `hourglass_flowing_sand` | Holmes running |
| `white_check_mark` | RCA or Ask complete |
| `large_green_circle` | this notification resolved |
| `x` | operation failed |

Holmes failures for both RCA and Ask produce a thread reply containing the
actual sanitized and bounded error. Active Alert Verification distinguishes an
Alertmanager query error from a successful query with zero matches; both stop
before Holmes and produce `x`. Reaction failures do not prevent investigation
or replies.

Watchdog is an ordinary firing alert when routed to AlertLens. A dead man's
switch must not depend on the component whose path it is intended to test, so
AlertLens exposes no self-referential Watchdog heartbeat metric.

## Reliability Model

AlertLens deliberately accepts rare duplicate work after Slack redelivery and
does not promise continuity after Slack retention or deletion. There is no
local disk or external database.

Reliability comes from a small failure surface:

- bounded in-memory queue;
- independent work across Slack threads;
- in-process serialization within one thread;
- bounded Alertmanager retry and Holmes concurrency;
- graceful draining on shutdown;
- Alertmanager-to-Slack delivery remains independent.

External state should be introduced only if future evidence requires durable
incident history or active-active duplicate suppression. It is not needed for
the current product contract.

## Security and Deployment

AlertLens sends bounded, delimited, sanitized advisory data to Holmes and
sanitizes output before Slack. It never runs remediation. Holmes toolsets and
RBAC enforce read-only investigation.

The Kubernetes deployment is stateless, non-root, read-only, and has no
service-account token or PVC. Secrets are referenced from an existing Secret.
NetworkPolicy limits egress to DNS, Slack HTTPS, Alertmanager, and HolmesGPT.

## MVP Behaviors

```gherkin
Given Alertmanager posts a valid firing notification in a monitored Slack channel
And Alertmanager has a matching active alert
When AlertLens receives it
Then AlertLens performs one automatic investigation for that notification
And posts the RCA in that notification's thread
```

```gherkin
Given Alertmanager verification cannot query Alertmanager
When a firing notification is handled
Then AlertLens posts the actual sanitized query error
And marks the notification failed without calling Holmes
```

```gherkin
Given Alertmanager verification finds no matching active alert
When a firing notification is handled
Then AlertLens posts an explicit no-match failure
And marks the notification failed without calling Holmes
```

```gherkin
Given a valid resolved notification
When AlertLens receives it
Then AlertLens adds large_green_circle only to that notification
```

```gherkin
Given any Slack thread in a monitored channel
When a user explicitly mentions AlertLens
Then AlertLens reconstructs eligible context from Slack
And asks Holmes without querying Alertmanager
And posts the answer in that thread
```

```gherkin
Given AlertLens is unavailable
When Alertmanager sends an alert
Then operators still receive the authoritative Slack notification
```
