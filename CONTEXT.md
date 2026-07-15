# AlertLens

AlertLens enriches authoritative Alertmanager notifications with investigation context while keeping alert delivery independent of AlertLens.

## Language

**Synthetic Alert**:
An intentionally injected Alertmanager alert that follows normal alert routing but does not represent a real incident.
_Avoid_: Fake alert, test event

**Monitored Channel**:
The single Slack channel assigned to an AlertLens installation. Automatic alert handling and explicit questions are ignored outside it.
_Avoid_: Monitored channels, channel allowlist, workspace, global channel

**Investigation Workspace**:
The Slack thread where responders read AlertLens investigation results and continue with Ask. For an Automatic Investigation, Alertmanager independently delivers the same alert to PagerDuty, which owns paging, acknowledgement, and resolution; its incident is not a second AlertLens conversation surface.
_Avoid_: PagerDuty conversation, dual-channel RCA

**Alert Identity**:
The correlation identity formed from an alert name and a namespace field whose value may be empty. An empty namespace means the alert is cluster-scoped; no synthetic namespace value is introduced. The marker must still contain `namespace=` so its two identity components are explicit.
_Avoid_: Alert ID, global namespace

**Notification Group**:
A set of alert instances grouped by Alertmanager for notification delivery. Multiple notification groups may belong to one Alert Identity.
_Avoid_: Alert Identity

**Notification Status**:
The `firing` or `resolved` status of one Alertmanager Notification Group, carried in the AlertLens marker alongside—but not as part of—Alert Identity. Firing starts Active Alert Verification; resolved only receives a `large_green_circle` reaction. A missing or unknown status fails with an `x` reaction and causes no Alertmanager or Holmes request.
_Avoid_: Alert lifecycle state, status as identity

**Active Alert Verification**:
The point-in-time prerequisite performed by AlertLens before asking Holmes to run an Automatic Investigation: Alertmanager must successfully return at least one current active alert matching Alert Identity; silenced and inhibited matches still count as active. A failed query or zero matches ends the operation with an `x` and a distinct Failure Reply; a later resolution does not cancel an investigation that already passed verification.
_Avoid_: Best-effort enrichment, Slack-only confirmation

**Verified Alert Snapshot**:
The bounded matching-alert data passed to Holmes after Active Alert Verification. It always preserves the successful verification fact and Alert Identity; instance details may be truncated without invalidating verification.
_Avoid_: Full Alertmanager response, unverified enrichment

**Inline Runbook**:
Troubleshooting guidance embedded directly in an alert and available as part of its Verified Alert Snapshot.
_Avoid_: Runbook URL, Holmes Skill

**Thread History**:
The messages currently available in a Slack thread. Content deleted, edited away, or expired under Slack retention is not part of the history AlertLens can continue from.
_Avoid_: Thread memory

**Conversation Context**:
The root message, prior explicit questions addressed to AlertLens, and AlertLens answers currently available in a thread, capped at 256 KiB before being sent to Holmes. The current Ask is sent separately and is not repeated in history. AlertLens reads every Slack page through the current Ask, filters eligible messages, retains the root, then includes the newest eligible messages until that byte limit; there is no separate message-count or page-count limit. Other thread discussion is excluded. If any page cannot be read, the current Ask fails without calling Holmes.
_Avoid_: Full thread, thread memory, conversation turn limit

**Thread Conversation ID**:
The Holmes request metadata derived from a Slack channel ID and thread root timestamp: `slack:<channel_id>:<root_ts>`. It groups usage and tool context but does not store or recover conversation history; AlertLens always supplies history reconstructed from Slack. Alert Identity may identify the request source, but does not identify the Holmes conversation.
_Avoid_: Alert Identity as conversation ID

**Automatic Investigation**:
An RCA started for each notification whose marker has `status=firing` after Active Alert Verification succeeds. It calls Holmes with the notification event's root message and Verified Alert Snapshot, which may span multiple Notification Groups. The root identifies the group that triggered this investigation. Automatic Investigation does not read Slack Thread History or couple its query to Alertmanager `group_by` fields.
_Avoid_: Cooldown, lifecycle suppression, stored alert episode

**Scheduled Investigation**:
A recurring investigation identified by an installation-unique name and defined by its own schedule and prompt; it has no Alert Identity and does not perform Active Alert Verification. Each trigger starts a run by creating an Investigation Workspace in the Monitored Channel; every subsequent result or reportable failure is delivered there, where users may continue with ordinary Asks and Slack-derived Conversation Context.
_Avoid_: CronJob, Scheduled Investigation ID, scheduled Ask, scheduled job

**Watchdog**:
An ordinary firing alert if it reaches a Monitored Channel. AlertLens has no Watchdog-specific branch or heartbeat metrics; an end-to-end dead man's switch must be observed by an independent monitoring path.
_Avoid_: AlertLens heartbeat, self-monitored dead man's switch

**Ask**:
An explicit `@AlertLens` question in a Monitored Channel. Every Ask follows the same path—even in an alert thread: AlertLens reconstructs Conversation Context from Slack and calls Holmes without Active Alert Verification or branching on an AlertLens marker. Holmes may use its configured tools to inspect current system state.
_Avoid_: Alert follow-up session, marker-specific Ask, Alertmanager-enriched Ask

**Failure Reply**:
A thread reply containing a sanitized and length-bounded failure reason. Active Alert Verification distinguishes a query's actual failure reason from an explicit zero-match reason; a Holmes failure reports its actual reason, and each case marks the operation with an `x` reaction.
_Avoid_: Generic failure message, fixed timeout reason, reaction-only failure

**Holmes Response Language**:
The language policy for successful Holmes answers, expressed as `auto` or any non-empty language tag. `auto` is the default and leaves the language to Holmes. It does not govern AlertLens warnings, failure replies, or truncation notices.
_Avoid_: AlertLens language, interface locale

**Stateless Processing**:
AlertLens reconstructs each operation from the current Slack event, current Slack Thread History when handling an Ask, and current Alertmanager data when handling firing. It has no state file, session store, lifecycle record, alert snapshot, thread mapping, or persisted Event Receipt. Transient queues, concurrency limits, and in-process locks are coordination rather than memory. Because event IDs are not persisted, a rare Slack redelivery may produce duplicate work or replies.
_Avoid_: Persistent memory, receipt-only state, restart continuity
