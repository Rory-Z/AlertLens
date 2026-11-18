# Untangle AlertLens

Scope: the whole repository, including comment and test cleanup. Preserve the
contracts in `CONTEXT.md` and the existing ADRs. No deployment or publication is
part of this refactor.

## Baseline

- Starting commit: `3340892`.
- The worktree was clean.
- `go test -race -coverprofile=/tmp/alertlens-untangle-baseline-coverage.out ./...`
  passed. Total statement coverage: 91.8%.
- Existing imports: `cmd/alertlens` assembles all components; `service` imports
  Alertmanager, Holmes, markers, metrics and cron; the Slack adapter imports
  `service` only for message types and failure text. Configuration and service
  independently own Scheduled Investigation types and cron parsers.

## Decisions and problems

The locations below refer to the starting commit.

1. Put the Slack message contract in `internal/slackmsg`. Both the adapter and
   the service depend on this small package. Keep sanitization at the service
   trust boundary.
   - `internal/slack/client.go:15,106,149` imports the orchestration package for
     event/history types and enumerates service-owned failure text.
   - `internal/service/service.go:24,32` owns Slack message vocabulary and types
     that change independently of worker orchestration.
   - `internal/service/prompt.go:162-244` combines Slack output formatting with
     Holmes input construction.
   - `internal/service/prompt.go:193,199` copies the ten-part limit into labels
     and character budgeting instead of using its constant.
   - `internal/service/service.go:460` names a public conversation-ID convention
     after its incidental use as a lock key.
2. Let `internal/scheduled` own normalized Scheduled Investigations, strict file
   loading and compiled UTC cron schedules. Configuration and service share the
   definition; the entrypoint does no field-by-field translation.
   - `internal/config/config.go:19` and `internal/service/service.go:55` duplicate
     the definition.
   - `cmd/alertlens/main.go:65` copies every field; adding model selection already
     required changes in all three places.
   - `internal/config/config.go:144-164` and `internal/service/service.go:188-197`
     define the cron grammar twice. Startup then reparses an already validated
     expression and can silently omit a configured run after logging an error.
   - `internal/config/config.go:113-177` mixes environment loading with an
     independent YAML-file format and its validation.
3. Group each service workflow with the code it changes. Keep the queue and
   shutdown lifecycle in `service.go`, alert verification/request construction
   in `alert.go`, Ask/history in `conversation.go`, Scheduled Investigations in
   `scheduled.go`, and Holmes answer delivery in `delivery.go`.
   - `internal/service/service.go:145,172,244,300` scatters scheduled intake,
     failure reporting and execution among queue and other workflow code.
   - Scheduled outcomes repeat metric/log fields at `service.go:148,178,251,315`.
   - `internal/service/service.go:370` only forwards to `runHolmesFrom`. Merge
     them and pass the actual current reaction at the three call sites.
   - `internal/holmes/response.go:371` forwards `takeByte` directly to the
     reader, while other methods already call the reader. Use the reader.
   - `internal/holmes/response.go:183` checks for an invalid rune that neither
     `ReadRune` nor the validated escape decoder can produce.
4. Make delivery configuration name the actual network destination.
   - `Makefile:17` supplies Holmes Service port 80 to
     `scripts/e2e-deploy:59`, which uses it as a NetworkPolicy Pod port.
     `README.md` already distinguishes Service 80 from target Pod 5050.
     Preserve the variable name, use 5050 and document its meaning.
   - `charts/alertlens/templates/_helpers.tpl:15-16,22-23` defines the same
     selector labels twice. Reuse the existing selector-label helper.
5. Clean tests last, using behavior as the assertion boundary.
   - `internal/service/integration_test.go:170` waits for a Holmes call before
     inspecting replies that the worker has not necessarily posted yet.
   - `internal/service/integration_test.go:895` compares context identity where
     the contract is a shared reply deadline.
   - `internal/service/prompt_test.go:42` claims to check runbook deduplication
     but gives too little budget to include any second runbook.
   - `internal/slack/client_test.go:55` asserts private adapter representation.
   - `charts/alertlens/tests/deployment_test.yaml:17` requires an omitted strategy
     field rather than testing the Deployment behavior.
   - `charts/alertlens/tests/configmap_test.yaml:25` fixes serialized YAML field
     order and scalar style. Check decoded content with existing tooling if
     possible; do not add a test framework for this assertion.
   - `internal/holmes/client_test.go:150` fixes the reader's buffer size rather
     than the bounded, complete UTF-8 prefix returned to callers. Test that
     response contract instead.
   - `internal/observability/metrics_test.go:21` reaches into the private registry
     instead of observing the metrics HTTP endpoint.
6. Once a bounded analysis string is full, continue validation without appending
   later characters. The new response-contract test exposed this during test
   cleanup; it was not a separate bug hunt.
   - `internal/holmes/response.go:183` tests each rune against the remaining
     budget independently. After dropping an oversized multibyte rune it can
     append a later ASCII rune, so the returned text is no longer a prefix.
   - Before the fix, a string of `界` followed by `x` returned 4,194,304 bytes;
     the complete prefix is 4,194,303 bytes. Keep the scanner's existing
     `tooLarge` flag as the decision to stop retaining text, while still
     consuming and validating the complete JSON string.

## Special cases to retain

- Explicit mentions take precedence over pasted markers; malformed markers fail
  and ordinary discussion is ignored. Resolved notifications only get a green
  reaction. Ask never performs Active Alert Verification.
- Old Alertmanager failure prefixes remain meaningful in retained Slack history.
- Scheduled root creation is not retried. Queue rejection and forced shutdown
  still report in the new root's thread. Queued shutdown replies share one deadline.
- API retries are bounded and cancelable: Alertmanager transient failures and
  Slack's explicit rate-limit response. Holmes requests are not retried.
- The bounded Holmes JSON scanner must skip arbitrary metadata without retaining
  it, preserve a complete analysis when later envelope validation fails, and
  distinguish oversized analysis from malformed responses.
- YAML v2 skips custom unmarshalling for null. Typed decoding plus a presence
  pass remains the smallest way to reject explicit null models while preserving
  strict schema checking. Keep both passes inside the Scheduled Investigation loader.
- Fixed-size thread lock stripes bound coordination memory. Socket adaptation
  converts the SDK's event field into a replaceable transport interface.
- Short waits in concurrency tests observe work that must remain blocked. Mutex
  waits are not durably blocked in Go's `testing/synctest`; do not replace these
  waits with `synctest.Wait` or change the production lock just to test it.
- Real-system E2E remains opt-in: it needs credentials, deployed services and a
  human Slack action. Its polling and port-forward cleanup reflect external
  asynchronous processes.
- Ecosystem-specific version fields follow the release ADR and stay separate.
- Initial comment inspection found only external setup guidance and the required
  `//go:embed` directive; these stay.

## Verification and review

All six decisions above are implemented.

- `internal/slackmsg` owns the shared event/history types, conversation ID,
  failure vocabulary and bounded Slack formatting. The Slack adapter no longer
  imports `service`.
- `internal/scheduled` owns strict file loading and the compiled cron schedule.
  `config` and `service` share its definition; the entrypoint conversion and
  second cron parse are gone.
- Service workflows now live with their request construction or delivery code.
  Scheduled metrics and logs have one owner. The forwarding Holmes method and
  reader method are gone.
- The E2E NetworkPolicy uses Holmes Pod port 5050, while its URL retains Service
  port 80. Shared Helm labels reuse the selector helper.
- Tests now observe final replies/reactions, shared deadlines, decoded chart
  configuration and public metrics. Runbook deduplication has an effective
  assertion. Slack formatting tests moved beside their protocol boundary.
- Oversized Holmes analysis remains a contiguous, complete UTF-8 prefix.
  The scanner still validates the rest of the response.
- The requested comment pass found no ordinary implementation-restating
  comments to remove. Compiler/linter directives, manual setup guidance and
  explanations of external library constraints remain.
- No dependency version changed. `prometheus/common`, already present, is now
  marked direct because the metrics HTTP contract test uses its text decoder.

Checks on the completed worktree:

| Check | Result |
| --- | --- |
| `go test -race -coverprofile=coverage.out ./...` | Passed for every package. |
| `go tool cover -func=coverage.out` | 92.2% total, above the unchanged 90% gate; baseline was 91.8%. |
| `test -z "$(gofmt -l .)"` | Passed. |
| `go vet ./...` | Passed. |
| `go mod tidy -diff` | Passed, with no pending module changes. |
| `go build -o /tmp/alertlens-untangle ./cmd/alertlens` | Passed. |
| `golangci-lint run --new-from-rev=HEAD ./...` | Passed, zero new issues. |
| `helm lint charts/alertlens` with the README's required values | Passed. |
| `helm unittest charts/alertlens` | Four suites, six tests passed. |
| `shellcheck scripts/*` | Passed. |
| `git diff --check` | Passed. |

The chart contract test uses real Helm rendering and the production
`scheduled.Load` parser, including literal multiline prompts. CI now installs
Helm before the Go suite so this check cannot silently skip there. The E2E
deployment regression test executes the real Makefile/script with replacement
`kubectl` and `helm` executables; before the port fix it observed 80 instead of
5050, and after the fix it passes. Neither test accesses a cluster.

`TestRunTriggersLoadedSchedulesWithoutStartupRun` assembles the real loader,
service and cron scheduler. Virtual time verifies that startup does not run an
investigation, then checks both configured prompts/models at the next minute
and delivery into their separate root threads. The existing UTC boundary test
also supplies a non-UTC input time.

The unrestricted `golangci-lint run ./...` still reports five baseline
diagnostics: two unchecked HTTP response-body closes and three ST1005 errors
about capitalized product names in error strings. Running the same linter on a
separate archive of starting commit `3340892` reproduces all five. They are not
new structural problems or changes in this refactor. Local golangci-lint is
2.11.4; CI declares 2.12.2.

Initial reviewers covered service/Slack, configuration/API boundaries, and
delivery. Subsequent independent reviews checked the new dependencies and
workflow invariants. The final reviewer read the current source, tests and
import graph and found no remaining structural issue worth fixing. The
retained special cases above each express a current domain or external-system
constraint.

Not run: real-system E2E, a container build, remote GitHub CI, publication or
deployment. Those require external state and are outside this local refactor.
