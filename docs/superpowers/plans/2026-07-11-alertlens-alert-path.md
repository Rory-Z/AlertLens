# AlertLens Alert Path Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the first production-shaped alert path from a marked Slack Socket Mode event through Alertmanager and HolmesGPT back into the original Slack thread.

**Architecture:** Parse alert identity at the boundary, query one Alertmanager with a concrete HTTP client, and call HolmesGPT 0.35.0 through `/api/chat`. A small orchestration service owns the queue, reaction transitions, session claim, prompt bounds, and reply; the Slack adapter only ACKs/translates events and performs Web API operations.

**Tech Stack:** Go 1.25, standard-library HTTP/testing, `github.com/slack-go/slack` v0.27.0, Slack Socket Mode, Alertmanager API v2, HolmesGPT 0.35.0.

## Global Constraints

- AlertLens only queries the configured Alertmanager; it never places an Alertmanager URL in a Slack marker or Holmes prompt.
- The new marker is `<!-- alertlens:alertname=<name>,namespace=<namespace> -->`; the legacy `vigil:` marker remains accepted and its `status` is ignored.
- Empty namespace is valid; alert name is required; the session key is `am:<alertname>:<namespace>`.
- Holmes requests contain no model, provider credential, or Holmes API key.
- Alert payload is at most 32768 bytes, deduplicated inline runbooks total at most 8192 bytes, and Slack output is at most 2500 characters.
- Slack, alert labels, annotations, and runbooks are untrusted prompt content and are visibly delimited from the read-only system instruction.
- A session is persisted before HolmesGPT is invoked; an already-active completed session suppresses repeat RCA.
- ACK happens before event processing; reaction failures do not fail the RCA.
- This milestone handles firing, duplicate firing, Watchdog, and failure reactions. Resolved, follow-up, ad-hoc, TTL dedup, metrics, per-session ordering, and retry refinements remain milestone 3.
- Every commit follows Conventional Commits 1.0.0.
- Repository statement coverage never drops below 90%.

---

### Task 1: Alert marker parser

**Files:**
- Create: `internal/marker/marker.go`
- Create: `internal/marker/marker_test.go`

**Interfaces:**
- Produces: `marker.Alert{Alertname string, Namespace string}`.
- Produces: `marker.Parse(text string) (marker.Alert, bool)`.
- Produces: `marker.Alert.Key() string`, returning `am:<alertname>:<namespace>`.
- Consumed by the service before it accepts work or adds a reaction.

- [x] **Step 1: Write failing boundary tests**

Create table tests covering the new marker, legacy marker with either status, HTML-escaped Slack text, whitespace/newlines, empty namespace, malformed key/value pairs, missing namespace, empty alert name, and unrelated text:

```go
func TestParse(t *testing.T) {
	tests := []struct {
		name string
		text string
		want Alert
		ok   bool
	}{
		{name: "new", text: `alert <!-- alertlens:alertname=HighCPU,namespace=prod -->`, want: Alert{"HighCPU", "prod"}, ok: true},
		{name: "legacy ignores status", text: `<!-- vigil:alertname=HighCPU,namespace=prod,status=resolved -->`, want: Alert{"HighCPU", "prod"}, ok: true},
		{name: "escaped", text: `&lt;!-- alertlens:alertname=Watchdog,namespace= --&gt;`, want: Alert{"Watchdog", ""}, ok: true},
		{name: "missing namespace", text: `<!-- alertlens:alertname=HighCPU -->`},
		{name: "empty alert name", text: `<!-- alertlens:alertname=,namespace=prod -->`},
		{name: "unrelated", text: "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.text)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("Parse() = (%#v, %v), want (%#v, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestAlertKey(t *testing.T) {
	if got := (Alert{Alertname: "NodeDown"}).Key(); got != "am:NodeDown:" {
		t.Fatalf("Key() = %q", got)
	}
}
```

- [x] **Step 2: Run the parser tests and verify RED**

Run: `go test ./internal/marker`

Expected: FAIL because `Alert` and `Parse` do not exist.

- [x] **Step 3: Implement the smallest parser**

Use `html.UnescapeString`, one compiled regexp for `alertlens|vigil`, and comma-separated first-`=` key/value parsing. Require both `alertname` and `namespace` keys, trim their values, and reject an empty alert name. Do not parse status or add source/provider abstractions.

- [x] **Step 4: Verify and commit**

Run: `gofmt -w internal/marker && go test ./internal/marker && go test ./...`

Expected: PASS.

```bash
git add internal/marker
git commit -m "feat(marker): parse Alertmanager Slack markers"
```

---

### Task 2: Alertmanager and HolmesGPT HTTP clients

**Files:**
- Create: `internal/alertmanager/client.go`
- Create: `internal/alertmanager/client_test.go`
- Create: `internal/holmes/client.go`
- Create: `internal/holmes/client_test.go`

**Interfaces:**
- Produces: `alertmanager.Alert` with labels, annotations, `startsAt`, `endsAt`, and `generatorURL` JSON fields.
- Produces: `alertmanager.New(baseURL *url.URL, timeout time.Duration) *Client`.
- Produces: `(*alertmanager.Client).Active(ctx context.Context, alertname, namespace string) ([]alertmanager.Alert, error)`.
- Produces: `holmes.Request` matching HolmesGPT 0.35.0 request field names.
- Produces: `holmes.New(baseURL *url.URL, timeout time.Duration) *Client` and `(*Client).Chat(context.Context, Request) (string, error)`.

- [x] **Step 1: Write failing Alertmanager contract tests**

Use `httptest.Server` to assert the exact request and filtering behavior:

```go
func TestActive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/alerts" || r.URL.Query().Get("active") != "true" ||
			r.URL.Query().Get("silenced") != "false" || r.URL.Query().Get("inhibited") != "false" {
			t.Fatalf("request = %s", r.URL.String())
		}
		_, _ = io.WriteString(w, `[
          {"labels":{"alertname":"HighCPU","namespace":"prod","pod":"api-0"},"annotations":{"runbook":"check cpu"},"startsAt":"2026-07-11T00:00:00Z","endsAt":"0001-01-01T00:00:00Z","generatorURL":"http://prom/graph"},
          {"labels":{"alertname":"Other","namespace":"prod"},"annotations":{},"startsAt":"2026-07-11T00:00:00Z","endsAt":"0001-01-01T00:00:00Z","generatorURL":""}
        ]`)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	alerts, err := New(base, time.Second).Active(context.Background(), "HighCPU", "prod")
	if err != nil || len(alerts) != 1 || alerts[0].Labels["pod"] != "api-0" {
		t.Fatalf("Active() = (%#v, %v)", alerts, err)
	}
}
```

Add cases for empty namespace matching a missing label, non-2xx status, malformed/oversized JSON, timeout, and two transient 5xx responses followed by success. Assert there are at most three total attempts and no retry on a 4xx response.

- [x] **Step 2: Verify Alertmanager RED and implement the client**

Run: `go test ./internal/alertmanager`

Expected: FAIL because the package implementation does not exist.

Implement a concrete client with its own `http.Client{Timeout: timeout}`. Resolve `/api/v2/alerts` relative to the configured base URL, set the three query parameters, limit the response body to 4 MiB, and decode complete JSON. Retry network errors and 5xx responses up to three total attempts with context-aware 100 ms then 200 ms delays; do not retry 4xx or JSON errors. Filter locally by exact `alertname` and namespace; a missing namespace label equals empty namespace.

- [x] **Step 3: Write failing HolmesGPT 0.35.0 contract tests**

The request type is exact and intentionally omits model/key fields:

```go
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Request struct {
	Ask                    string    `json:"ask"`
	ConversationHistory    []Message `json:"conversation_history,omitempty"`
	AdditionalSystemPrompt string    `json:"additional_system_prompt,omitempty"`
	RequestSource          string    `json:"request_source,omitempty"`
	SourceRef              string    `json:"source_ref,omitempty"`
	ConversationID         string    `json:"conversation_id,omitempty"`
}
```

Use `httptest.Server` to decode the request, assert `POST /api/chat`, `Content-Type: application/json`, `request_source=alert_investigation`, and absence of `model`, `api_key`, and authorization headers. Return `{"analysis":"root cause"}` and assert the client returns `root cause`. Add tests for timeout, non-2xx, malformed/oversized response, and empty analysis. Assert the server receives exactly one request after a failure.

- [x] **Step 4: Verify Holmes RED and implement the client**

Run: `go test ./internal/holmes`

Expected: FAIL because `Request`, `New`, and `Chat` do not exist.

Implement one non-retrying POST with the configured timeout, a 4 MiB response cap, status validation, and decoding only the `analysis` field. Never accept or add an API key option.

- [x] **Step 5: Run the clients under race and commit**

Run: `gofmt -w internal/alertmanager internal/holmes && go test -race ./internal/alertmanager ./internal/holmes && go test ./...`

Expected: PASS.

```bash
git add internal/alertmanager internal/holmes
git commit -m "feat(api): add Alertmanager and Holmes clients"
```

---

### Task 3: Firing-alert orchestration with the testing trophy

**Files:**
- Create: `internal/service/service.go`
- Create: `internal/service/prompt.go`
- Create: `internal/service/integration_test.go`
- Create: `internal/service/prompt_test.go`
- Modify: `internal/session/model.go`

**Interfaces:**
- Consumes: `marker.Parse`, `Alertmanager.Active`, `Holmes.Chat`, and `session.Store`.
- Produces: `service.Event{ID, Channel, User, BotID, Text, TS, ThreadTS string}`.
- Produces: `service.Alertmanager` with `Active(context.Context, string, string) ([]alertmanager.Alert, error)` and `service.Holmes` with `Chat(context.Context, holmes.Request) (string, error)`; the HTTP clients satisfy them directly.
- Produces: `service.Slack` with `AddReaction(context.Context, string, string, string) error`, `RemoveReaction(context.Context, string, string, string) error`, and `Reply(context.Context, string, string, string) error`; the fake test implementation is the second implementation.
- Produces: `service.Config` with integer fields `QueueSize`, `Workers`, `AlertPayloadMaxBytes`, `RunbookMaxBytes`, `SlackOutputMaxChars`, plus `AlertSessionTTL time.Duration`, rather than exposing the full secret-bearing application config.
- Produces: `service.New(store, alertmanager, holmes, slack, config, now) *Service`, `(*Service).Submit(context.Context, Event) bool`, and `(*Service).Run(context.Context)`.

- [ ] **Step 1: Write one end-to-end in-process firing test**

Assemble the real marker parser, service, HTTP clients, session store, two `httptest.Server` instances, and a fake Slack transport. Submit a marked Slack event and assert:

```go
wantReactions := []string{
	"add:eyes:C1:100.1",
	"remove:eyes:C1:100.1",
	"add:hourglass_flowing_sand:C1:100.1",
	"remove:hourglass_flowing_sand:C1:100.1",
	"add:white_check_mark:C1:100.1",
}
```

The Holmes handler must read the on-disk snapshot before responding and find an active `am:HighCPU:prod` session, proving persistence precedes the side effect. Assert its request contains all matching alerts, one deduplicated inline runbook, delimited original Slack text, `request_source=alert_investigation`, `source_ref=am:HighCPU:prod`, and a stable conversation ID. Assert the fake Slack reply targets thread `100.1` and is bounded.

- [ ] **Step 2: Run the integration test and verify RED**

Run: `go test ./internal/service -run TestFiringAlert -v`

Expected: FAIL because the service does not exist.

- [ ] **Step 3: Implement minimal queue and firing flow**

`Submit` parses before acceptance, adds `eyes`, and performs a non-blocking send to a channel sized by `EVENT_QUEUE_SIZE`. On a full queue it removes `eyes`, adds `x`, and returns false. `Run` starts exactly `HOLMESGPT_MAX_CONCURRENCY` workers and stops on context cancellation.

For a normal firing work item:

1. Query Alertmanager.
2. On error, replace `eyes` with `x`.
3. If no alerts match, remove `eyes` and stop; resolved behavior is milestone 3.
4. Atomically claim the session with `Store.Update`; persist channel, parent timestamp, bounded alert JSON, active state, timestamps, and expiry before Holmes.
5. If an active session already exists, refresh its expiry, remove `eyes`, and do not call Holmes.
6. Replace `eyes` with `hourglass_flowing_sand`.
7. Build and send the bounded Holmes request.
8. Sanitize obvious credential assignments and bearer/token patterns, truncate on UTF-8 rune boundaries to `SLACK_OUTPUT_MAX_CHARS` with a truncation notice, and reply in the parent thread.
9. Replace the hourglass with `white_check_mark` and persist the final assistant turn.

Reaction calls are best-effort and never contain message text in errors or logs. A Watchdog marker queries Alertmanager, records no Holmes call or reply, and transitions directly from `eyes` to `white_check_mark`; its metrics are milestone 3.

- [ ] **Step 4: Add integration cases for duplicate and failures**

Use the same assembled system to cover:

- repeated active notification: one Holmes call and one reply total;
- Alertmanager 500: no Holmes call, no false state, final `x`;
- Holmes 500: one Holmes call, persisted active session, final `x`, no automatic retry;
- queue saturation: rejected submit receives `x`;
- Watchdog: no Holmes call or reply;
- reaction failure: RCA and reply still succeed.

- [ ] **Step 5: Add focused prompt boundary tests**

Test complete-alert JSON bounding, runbook deduplication and byte limits, untrusted delimiters, UTF-8 Slack truncation, and sanitizer replacement. Never slice arbitrary JSON bytes; retain as many complete alerts as fit and re-marshal.

- [ ] **Step 6: Verify race, coverage, and commit**

Run:

```bash
gofmt -w internal/service internal/session
go test -race ./internal/service
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | awk '/^total:/ { gsub("%", "", $3); if ($3 + 0 < 90) exit 1 }'
```

Expected: PASS and total coverage at least 90%.

```bash
git add internal/service internal/session
git commit -m "feat(service): process firing alerts"
```

---

### Task 4: Slack Socket Mode adapter

**Files:**
- Modify: `go.mod`
- Create: `go.sum`
- Create: `internal/slack/client.go`
- Create: `internal/slack/client_test.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/config.go`

**Interfaces:**
- Consumes: Slack app/bot tokens, configured channel set, and `func(context.Context, service.Event) bool`.
- Implements: `service.Slack` through Slack Web API calls.
- Produces: `slackadapter.New(botToken, appToken string, channels map[string]bool) *Client`, `(*Client).Run(context.Context, handler) error`, and `(*Client).Ready() error`.

- [ ] **Step 1: Add the supported Slack SDK**

Run: `go get github.com/slack-go/slack@v0.27.0`

Expected: `go.mod` and `go.sum` record v0.27.0 and its WebSocket dependency without changing the Go 1.25 baseline.

- [ ] **Step 2: Write failing event translation and Web API tests**

Test pure message translation for top-level text plus all attachment text, event ID/channel/timestamps, bot messages, thread messages, edited/deleted subtype rejection, unmonitored channel rejection, and self-user rejection. Use a Slack API `httptest.Server` to assert:

- `auth.test` discovers the bot user ID;
- `reactions.add` and `reactions.remove` receive channel/timestamp/name;
- `chat.postMessage` receives `thread_ts` and bounded text.

Add config tests rejecting non-`xoxb-` bot tokens and non-`xapp-` app tokens, following the Slack SDK's official Socket Mode example.

- [ ] **Step 3: Verify RED and implement the adapter**

Run: `go test ./internal/slack ./internal/config`

Expected: FAIL for the missing adapter and token validation.

Construct one `slack.Client` with `slack.OptionAppLevelToken`, then one `socketmode.Client`. `Run` calls `auth.test`, starts one event-consumer goroutine, tracks connected/disconnected readiness, and calls `AckCtx` immediately for every Events API envelope before translating or submitting it. The calling goroutine blocks in `socket.RunContext(ctx)` and returns its terminal error. Do not spawn one goroutine per event; `Submit` is intentionally fast and bounded.

Use `AddReactionContext`, `RemoveReactionContext`, and `PostMessageContext` with `slack.MsgOptionTS`. Treat `already_reacted` and `no_reaction` as success; return other errors so the service can count them later while continuing its main flow.

- [ ] **Step 4: Verify and commit**

Run: `gofmt -w internal/slack internal/config && go test -race ./internal/slack ./internal/config && go test ./...`

Expected: PASS.

```bash
git add go.mod go.sum internal/slack internal/config
git commit -m "feat(slack): add Socket Mode adapter"
```

---

### Task 5: Startup assembly and milestone gate

**Files:**
- Modify: `cmd/alertlens/main.go`
- Modify: `cmd/alertlens/main_test.go`
- Modify: `internal/health/handler_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes every concrete client from Tasks 2-4.
- Readiness is healthy only when both `session.Store.Ready()` and `slack.Client.Ready()` return nil.

- [ ] **Step 1: Write failing assembly tests**

Keep startup network-free in tests by passing an already-canceled context after configuration and store initialization. Assert invalid/corrupt state still fails before Slack starts. Add a readiness test combining a store error and a disconnected Slack error while ensuring `/readyz` always returns the generic `not ready` body.

- [ ] **Step 2: Wire concrete dependencies**

In `run`, create the session store, Alertmanager client, Holmes client, Slack client, and service directly; do not add factories or a dependency-injection framework. Start the service workers and HTTP server, then run Socket Mode until context cancellation or a terminal error. Shut down the HTTP server within five seconds. Readiness checks store first, then Socket Mode connection.

Update README status and document the required Slack scopes/events without claiming resolved or ad-hoc support yet:

- app token: `connections:write`;
- bot scopes: `app_mentions:read`, `channels:history`, `chat:write`, `reactions:read`, `reactions:write`;
- event subscriptions: `message.channels` and `app_mention` (the latter is used in milestone 3).

- [ ] **Step 3: Run the complete milestone verification**

Run:

```bash
test -z "$(gofmt -l .)"
go vet ./...
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | tee /tmp/alertlens-coverage.txt
awk '/^total:/ { gsub("%", "", $3); if ($3 + 0 < 90) exit 1 }' /tmp/alertlens-coverage.txt
go build ./cmd/alertlens
helm lint charts/alertlens --set slack.existingSecret=alertlens-slack --set-string 'slack.alertChannels[0]=C1' --set alertmanagerURL=http://alertmanager:9093 --set holmesURL=http://holmes:5050
helm unittest charts/alertlens
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 .github/workflows/ci.yaml
docker build -t alertlens:test .
```

Expected: all commands exit 0, coverage remains at least 90%, and the container builds with Go 1.25.12.

- [ ] **Step 4: Server-side dry-run the rendered release**

Render with placeholder Slack secret/channel values and apply using `--dry-run=server` with `~/.kube/flowmq-dev-tiger.yaml`. Expected: Kubernetes accepts every object and creates nothing.

- [ ] **Step 5: Commit the milestone wiring**

```bash
git add cmd README.md
git commit -m "feat(app): wire the firing alert path"
```

---

## Milestone 2 Completion Check

- A real Socket Mode envelope is ACKed before bounded submission.
- A marked firing alert is queried from Alertmanager, persisted, sent once to HolmesGPT, and replied to in its Slack thread.
- Reactions follow `eyes` -> `hourglass_flowing_sand` -> `white_check_mark`, with `x` on operation failure.
- Duplicate active notifications do not repeat HolmesGPT RCA.
- Watchdog does not invoke HolmesGPT.
- No Holmes/model credential or Alertmanager URL enters markers, prompts, state, logs, or metrics.
- Race, 90% coverage, Helm, actionlint, binary, image, and server-side Kubernetes dry-run gates pass.
- Resolved, ad-hoc/follow-up, detailed metrics, TTL event dedup, and real Slack smoke remain explicit later milestones.
