# AlertLens Ad-hoc and Follow-up Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Support explicit top-level and threaded `@AlertLens` questions without accepting ordinary human conversation or expanding AlertLens into an autonomous agent.

**Architecture:** The Slack adapter marks only Events API `app_mention` payloads as explicit mentions. The service correlates a threaded mention to an existing persisted session by channel/thread timestamp; otherwise it creates a concrete Slack ad-hoc session and uses the existing Holmes/reaction/persistence path.

**Tech Stack:** Existing Go 1.25 service, Slack Socket Mode adapter, HolmesGPT 0.35.0 `/api/chat`.

## Global Constraints

- Human messages without an explicit Slack `app_mention` are ignored.
- Top-level mentions reply in a new thread rooted at their own timestamp.
- Mentions in known alert threads reuse bounded alert context and conversation.
- Mentions in unknown threads create an ad-hoc session on that existing thread.
- Ad-hoc keys are `slack:<channel>:<parent-ts>`; Alertmanager keys remain unchanged.
- Ad-hoc sessions expire after eight idle hours by default.
- Conversation retains at most six user/assistant messages and 16384 bytes; only user questions and final Holmes answers are persisted.
- Holmes receives one leading system message when conversation history is present, satisfying HolmesGPT 0.35.0 validation.
- The reaction flow is `eyes` -> `hourglass_flowing_sand` -> `white_check_mark` or `x`.
- No GitOps, shell, cron, private Git, PagerDuty, or non-mention message behavior is added.
- Conventional Commits and repository coverage >=90% remain mandatory.

---

### Task 1: Slack `app_mention` translation

**Files:**
- Modify: `internal/service/service.go`
- Modify: `internal/slack/client.go`
- Modify: `internal/slack/client_test.go`

**Interfaces:**
- Adds `Mention bool` to `service.Event`.
- Extends `translate` to accept `*slackevents.AppMentionEvent` as well as message events.

- [x] Write failing tests for top-level and threaded `AppMentionEvent`, including event ID, channel, user, timestamp, thread timestamp, and bot mention removal from text. Assert ordinary `MessageEvent` remains `Mention=false`.
- [x] Run `go test ./internal/slack` and verify RED.
- [x] Implement the second concrete translation branch; channel/self filtering remains shared and edited message behavior stays unchanged.
- [x] Run race tests and commit:

```bash
git add internal/slack internal/service
git commit -m "feat(slack): translate explicit app mentions"
```

---

### Task 2: Ad-hoc sessions and alert-thread follow-ups

**Files:**
- Modify: `internal/service/service.go`
- Create: `internal/service/conversation.go`
- Modify: `internal/service/integration_test.go`
- Create: `internal/service/conversation_test.go`
- Modify: `cmd/alertlens/main.go`

**Interfaces:**
- Adds `AdhocSessionTTL time.Duration` and `ConversationMaxTurns int` to `service.Config`.
- Adds a private mention work type carrying the selected session key, parent thread, prior alert context, and bounded conversation.

- [x] Write a failing top-level ad-hoc integration test: explicit mention at `C1/10.1` persists `slack:C1:10.1` before Holmes, sends `request_source=freeform`, replies in thread `10.1`, and persists the user question plus final answer.
- [x] Implement top-level and unknown-thread session creation with `Type=adhoc`, `State=active`, and idle expiry.
- [x] Write a failing known-alert-thread test: seed `am:A:ns` at thread `100.1`, mention in that thread, and assert Holmes receives the stored alert JSON and conversation under the same `am:A:ns` conversation ID.
- [x] Implement session lookup by `Channel` and `ThreadTS`/`ParentTS`; serialize through the existing session lock shard.
- [x] Add tests for unknown-thread ad-hoc, ignored unmentioned human messages, Holmes/reply/persistence failures, and repeated explicit questions.
- [x] Add pure conversation tests for six-message and 16384-byte pruning. `conversation_history` begins with `{role:"system"}` and then bounded prior user/assistant messages; current question stays in `ask`.
- [x] Wire `ADHOC_SESSION_TTL` and `CONVERSATION_MAX_TURNS` from application config, then run race and coverage gates.
- [x] Commit:

```bash
git add internal/service cmd/alertlens
git commit -m "feat(service): answer ad-hoc and follow-up questions"
```

---

### Task 3: Documentation and milestone verification

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/plans/2026-07-11-alertlens-adhoc-followup.md`

- [ ] Document top-level, known-thread, and unknown-thread mention behavior and explicitly state that unmentioned messages are ignored.
- [ ] Run gofmt, vet, race+coverage >=90%, build, Helm lint/unit, actionlint, Docker build, and Kubernetes server-side dry-run.
- [ ] Mark this plan complete and commit:

```bash
git add README.md docs/superpowers/plans/2026-07-11-alertlens-adhoc-followup.md
git commit -m "docs(readme): document explicit Slack questions"
```

## Milestone 3B Completion Check

- Top-level explicit mentions create a thread and ad-hoc session.
- Known alert threads reuse alert and conversation context.
- Unknown threads create ad-hoc context on that thread.
- Unmentioned human messages remain ignored.
- Conversation remains within six messages and 16384 bytes across restart.
- Full CI-equivalent and Kubernetes server-side dry-run gates pass.
