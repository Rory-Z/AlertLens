# AlertLens Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce a runnable, testable Go service with validated configuration, durable atomic state storage, health/readiness endpoints, and deployable single-replica Helm packaging.

**Architecture:** This is milestone 1 of 4. Keep startup wiring in `cmd/alertlens`, parse environment variables with the standard library, and store one versioned JSON snapshot through a concrete filesystem store. Later milestones add the Slack/Alertmanager/Holmes flow without changing these boundaries.

**Tech Stack:** Go 1.22, standard library, Docker, Helm 3, GitHub Actions.

## Global Constraints

- AlertLens is a single Go binary and has no model-provider or Holmes API key.
- Required environment variables are `SLACK_BOT_TOKEN`, `SLACK_APP_TOKEN`, `SLACK_ALERT_CHANNELS`, `ALERTMANAGER_URL`, and `HOLMESGPT_URL`.
- `STATE_PATH` defaults to `/var/lib/alertlens/state.json`; state files use mode `0600` and atomic replacement.
- Startup fails for invalid configuration, corrupt state, or an unwritable state directory.
- The Kubernetes workload has one replica, `Recreate` strategy, an RWO PVC, no service-account token, a non-root user, and a read-only root filesystem.
- Use the standard library unless an external integration requires a dependency.
- Every commit follows [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/): `<type>(<scope>): <description>`; use `!` or a `BREAKING CHANGE:` footer for breaking changes.
- This milestone does not add Slack, Alertmanager, HolmesGPT, PagerDuty, Git, or VictoriaLogs behavior.
- Every task is test-first and ends with a commit.

## Rolling Milestones

1. This plan: Go foundation, state store, container, Helm, and CI.
2. Alert path: marker parsing and Alertmanager -> HolmesGPT -> Slack happy path.
3. Conversation and reliability: deduplication, follow-ups, ad-hoc, resolved, Watchdog, queueing, and recovery.
4. Release verification: complete testing trophy, security/limits, documentation, and isolated `flowmq-dev-tiger` smoke test.

---

### Task 1: Validated configuration and runnable health server

**Files:**
- Create: `go.mod`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `internal/health/handler.go`
- Create: `internal/health/handler_test.go`
- Create: `cmd/alertlens/main.go`
- Create: `cmd/alertlens/main_test.go`

**Interfaces:**
- Produces: `config.Load(getenv func(string) string) (config.Config, error)`
- Produces: `config.Config` with parsed URLs, durations, limits, monitored channel set, state path, language, and metrics address.
- Produces: `health.New(readiness func() error) http.Handler`
- Later tasks consume `config.Config.StatePath` and supply the state-store readiness function.

- [x] **Step 1: Initialize the Go module**

```go
module github.com/emqx/alertlens

go 1.22
```

- [x] **Step 2: Write failing table tests for required configuration and defaults**

Create `internal/config/config_test.go` with a helper backed by a map and cases that assert:

```go
func TestLoad(t *testing.T) {
	valid := map[string]string{
		"SLACK_BOT_TOKEN":       "xoxb-test",
		"SLACK_APP_TOKEN":       "xapp-test",
		"SLACK_ALERT_CHANNELS":  "C1, C2",
		"ALERTMANAGER_URL":      "http://alertmanager:9093",
		"HOLMESGPT_URL":         "http://holmes:5050",
	}

	t.Run("defaults", func(t *testing.T) {
		cfg, err := Load(mapEnv(valid))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.StatePath != "/var/lib/alertlens/state.json" {
			t.Fatalf("StatePath = %q", cfg.StatePath)
		}
		if cfg.HolmesMaxConcurrency != 4 || cfg.EventQueueSize != 100 {
			t.Fatalf("unexpected limits: %+v", cfg)
		}
		if !cfg.AlertChannels["C1"] || !cfg.AlertChannels["C2"] {
			t.Fatalf("channels = %#v", cfg.AlertChannels)
		}
	})

	t.Run("missing required value", func(t *testing.T) {
		env := maps.Clone(valid)
		delete(env, "HOLMESGPT_URL")
		if _, err := Load(mapEnv(env)); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("invalid URL", func(t *testing.T) {
		env := maps.Clone(valid)
		env["ALERTMANAGER_URL"] = "://bad"
		if _, err := Load(mapEnv(env)); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("invalid positive limit", func(t *testing.T) {
		env := maps.Clone(valid)
		env["EVENT_QUEUE_SIZE"] = "0"
		if _, err := Load(mapEnv(env)); err == nil {
			t.Fatal("expected error")
		}
	})
}
```

The test file imports `maps`, `testing`, and defines:

```go
func mapEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
```

- [x] **Step 3: Run the configuration test and verify it fails**

Run: `go test ./internal/config`

Expected: FAIL because `Load` and `Config` do not exist.

- [x] **Step 4: Implement configuration parsing with the standard library**

Create `internal/config/config.go`. Define one concrete `Config` struct containing all configuration names and defaults from the approved design. Implement `Load` with small private helpers for required strings, HTTP(S) base URLs, positive integers, positive durations, and comma-separated channel IDs.

The public shape is:

```go
type Config struct {
	SlackBotToken          string
	SlackAppToken          string
	AlertChannels          map[string]bool
	AlertmanagerURL        *url.URL
	HolmesURL              *url.URL
	StatePath              string
	ReplyLanguage          string
	AlertmanagerTimeout    time.Duration
	HolmesTimeout          time.Duration
	HolmesMaxConcurrency   int
	EventQueueSize         int
	EventDedupTTL          time.Duration
	AlertSessionTTL        time.Duration
	ResolvedSessionTTL     time.Duration
	AdhocSessionTTL        time.Duration
	AlertPayloadMaxBytes   int
	RunbookMaxBytes        int
	ConversationMaxTurns   int
	ConversationMaxBytes   int
	SlackOutputMaxChars    int
	MetricsAddr            string
}

func Load(getenv func(string) string) (Config, error)
```

Reject empty required values, URLs without `http` or `https`, non-positive durations and limits, and an empty channel list. Wrap errors with the environment-variable name; never include secret values in errors.

- [x] **Step 5: Run configuration tests**

Run: `go test ./internal/config`

Expected: PASS.

- [x] **Step 6: Write failing handler tests for liveness and readiness**

Create `internal/health/handler_test.go`:

```go
func TestHandler(t *testing.T) {
	readyErr := error(nil)
	h := New(func() error { return readyErr })

	assertStatus := func(path string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s status = %d, want %d", path, w.Code, want)
		}
	}

	assertStatus("/healthz", http.StatusOK)
	assertStatus("/readyz", http.StatusOK)
	readyErr = errors.New("state unavailable")
	assertStatus("/readyz", http.StatusServiceUnavailable)
	assertStatus("/unknown", http.StatusNotFound)
}
```

- [x] **Step 7: Run the handler test and verify it fails**

Run: `go test ./internal/health`

Expected: FAIL because `New` does not exist.

- [x] **Step 8: Implement the health handler and process startup**

Create `internal/health/handler.go` using `http.NewServeMux`. `/healthz` always returns status 200 and body `ok\n`. `/readyz` returns 200 only when the supplied function returns nil; otherwise it returns 503 and the generic body `not ready\n` without exposing the underlying error.

Create `cmd/alertlens/main.go` that:

```go
func main() {
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("alertlens stopped", "error", err)
		os.Exit(1)
	}
}
```

`run` loads config, starts an `http.Server` on `cfg.MetricsAddr`, handles `SIGINT` and `SIGTERM` with `signal.NotifyContext`, and shuts down with a five-second context. For this task, readiness returns nil after configuration succeeds. Do not log the `Config` struct because it contains Slack tokens.

- [x] **Step 9: Run and build the service**

Create `cmd/alertlens/main_test.go` with a valid map-backed environment, `METRICS_ADDR=127.0.0.1:0`, and a `STATE_PATH` under `t.TempDir()`. Cover both startup validation and graceful cancellation:

```go
func TestRun(t *testing.T) {
	t.Run("invalid config", func(t *testing.T) {
		if err := run(context.Background(), func(string) string { return "" }); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := run(ctx, mapEnv(validEnv(t))); err != nil {
			t.Fatal(err)
		}
	})
}
```

Keep `main` limited to signal setup, calling `run`, logging the returned error, and setting the exit code so all meaningful startup logic remains testable through `run`.

Run: `gofmt -w cmd internal && go test ./... && go build ./cmd/alertlens`

Expected: all packages PASS and the binary builds.

- [x] **Step 10: Commit the runnable foundation**

```bash
git add go.mod cmd/alertlens internal/config internal/health
git commit -m "feat(core): add Go service foundation"
```

---

### Task 2: Versioned atomic snapshot store and durable readiness

**Files:**
- Create: `internal/session/model.go`
- Create: `internal/session/store.go`
- Create: `internal/session/store_test.go`
- Modify: `cmd/alertlens/main.go`
- Modify: `internal/health/handler_test.go`

**Interfaces:**
- Consumes: `config.Config.StatePath`
- Produces: `session.Snapshot`, `session.Record`, and `session.ConversationTurn` JSON contracts.
- Produces: `session.Open(path string, now func() time.Time) (*session.Store, error)`
- Produces: `(*session.Store).Snapshot() session.Snapshot`
- Produces: `(*session.Store).Update(func(*session.Snapshot) error) error`
- Produces: `(*session.Store).Ready() error`
- Later milestones update all session and dedup state through `Store.Update`.

- [x] **Step 1: Write failing persistence integration tests**

Create `internal/session/store_test.go` with subtests for:

```go
func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	store, err := Open(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(snapshot *Snapshot) error {
		snapshot.Sessions["am:HighCPU:prod"] = Record{
			Key: "am:HighCPU:prod", Type: "alert", State: "active",
			Channel: "C1", ParentTS: "1.2", UpdatedAt: now,
		}
		snapshot.EventIDs["Ev1"] = now.Add(time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot().Sessions["am:HighCPU:prod"].ParentTS; got != "1.2" {
		t.Fatalf("ParentTS = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}
```

Add cases that assert `Open` rejects corrupt JSON and a future snapshot version, prunes expired event IDs and sessions, and leaves the old file unchanged when the update callback returns an error. Add a write-failure case that makes the directory read-only before `Update`, expects `Ready()` to become non-nil, restores permissions, performs a successful update, and expects readiness to recover. Skip that permission-specific case when `os.Geteuid() == 0` because root bypasses directory permissions.

- [x] **Step 2: Run the store test and verify it fails**

Run: `go test ./internal/session`

Expected: FAIL because the session types and `Open` do not exist.

- [x] **Step 3: Define the minimal versioned snapshot model**

Create `internal/session/model.go`:

```go
const CurrentVersion = 1

type Snapshot struct {
	Version  int                  `json:"version"`
	Sessions map[string]Record    `json:"sessions"`
	EventIDs map[string]time.Time `json:"eventIds"`
}

type Record struct {
	Key          string             `json:"key"`
	Type         string             `json:"type"`
	State        string             `json:"state"`
	Channel      string             `json:"channel"`
	ParentTS     string             `json:"parentTs"`
	ThreadTS     string             `json:"threadTs,omitempty"`
	AlertContext json.RawMessage    `json:"alertContext,omitempty"`
	Conversation []ConversationTurn `json:"conversation,omitempty"`
	CreatedAt    time.Time          `json:"createdAt"`
	UpdatedAt    time.Time          `json:"updatedAt"`
	ExpiresAt    time.Time          `json:"expiresAt,omitempty"`
}

type ConversationTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
```

Do not add PagerDuty fields, provider interfaces, database adapters, or migration machinery for nonexistent versions.

- [x] **Step 4: Implement serialized atomic persistence**

Create `internal/session/store.go` with a `sync.Mutex`, in-memory snapshot, state path, clock function, and readiness error. `Open` creates the parent directory when absent, verifies it is writable by creating and removing a temporary file, reads an existing snapshot when present, rejects corrupt or unsupported versions, initializes maps, prunes records whose non-zero `ExpiresAt` is not after `now()`, prunes expired event IDs, and persists the pruned result.

`Update` must:

1. Lock the store.
2. Deep-copy the current snapshot through JSON marshal/unmarshal.
3. Run the callback against the copy.
4. Marshal the full copy with a trailing newline.
5. Create a temporary file in the destination directory, chmod it to `0600`, write, `Sync`, close, rename over the destination, and sync the directory.
6. Replace in-memory state only after the rename succeeds.
7. Set the readiness error on persistence failure and clear it after success.

Use only `os`, `encoding/json`, `path/filepath`, `sync`, and other standard-library packages. Remove the temporary file on every failure path.

- [x] **Step 5: Run session tests**

Run: `go test ./internal/session`

Expected: PASS, with the permission test skipped only when running as root.

- [x] **Step 6: Wire store readiness into startup**

Modify `run` in `cmd/alertlens/main.go` to open the store before serving and construct the health handler with `store.Ready`. Startup must return the `Open` error. Do not expose the state path through HTTP or logs.

- [x] **Step 7: Verify durable readiness and the full repository**

Run: `gofmt -w cmd internal && go test -race ./... && go build ./cmd/alertlens`

Expected: PASS with no race reports.

- [x] **Step 8: Commit durable state**

```bash
git add cmd/alertlens internal/session internal/health
git commit -m "feat(session): persist state atomically"
```

---

### Task 3: Distroless image, Helm release, and continuous integration

**Files:**
- Create: `Dockerfile`
- Create: `.dockerignore`
- Create: `.github/workflows/ci.yaml`
- Create: `charts/alertlens/Chart.yaml`
- Create: `charts/alertlens/values.yaml`
- Create: `charts/alertlens/templates/_helpers.tpl`
- Create: `charts/alertlens/templates/serviceaccount.yaml`
- Create: `charts/alertlens/templates/deployment.yaml`
- Create: `charts/alertlens/templates/pvc.yaml`
- Create: `charts/alertlens/templates/service.yaml`
- Create: `charts/alertlens/templates/networkpolicy.yaml`
- Create: `charts/alertlens/templates/tests/test-connection.yaml`
- Create: `charts/alertlens/tests/deployment_test.yaml`
- Modify: `README.md`

**Interfaces:**
- Consumes: the binary from `./cmd/alertlens` and every environment variable in `config.Config`.
- Produces: image entrypoint `/alertlens` and Helm chart `charts/alertlens`.
- Produces: Kubernetes Service port `metrics` on 9090 and PVC mount `/var/lib/alertlens`.
- Later smoke verification supplies Slack tokens through an existing Secret and non-secret service URLs through values.

- [ ] **Step 1: Write failing Helm assertions**

Create `charts/alertlens/tests/deployment_test.yaml` for `helm-unittest` with assertions that the Deployment:

```yaml
suite: deployment
templates:
  - templates/deployment.yaml
tests:
  - it: renders one hardened Recreate replica with persistent state
    set:
      slack.existingSecret: alertlens-slack
      alertmanagerURL: http://alertmanager:9093
      holmesURL: http://holmes:5050
    asserts:
      - equal:
          path: spec.replicas
          value: 1
      - equal:
          path: spec.strategy.type
          value: Recreate
      - equal:
          path: spec.template.spec.automountServiceAccountToken
          value: false
      - equal:
          path: spec.template.spec.containers[0].securityContext.readOnlyRootFilesystem
          value: true
      - equal:
          path: spec.template.spec.containers[0].env[?(@.name == "STATE_PATH")].value
          value: /var/lib/alertlens/state.json
```

- [ ] **Step 2: Run Helm lint and verify it fails**

Run: `helm lint charts/alertlens`

Expected: FAIL because the chart does not exist.

- [ ] **Step 3: Add the minimal multi-stage image**

Create a multi-stage `Dockerfile` that builds with `golang:1.22` using `CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/alertlens ./cmd/alertlens`, then copies it into `gcr.io/distroless/static-debian12:nonroot`. Run as the distroless `nonroot` user and set `ENTRYPOINT ["/alertlens"]`.

Create `.dockerignore` containing only `.git`, the local binary, coverage files, and editor files.

- [ ] **Step 4: Implement the minimal Helm chart**

The chart must render:

- exactly one replica with `strategy.type: Recreate`;
- a dedicated ServiceAccount with `automountServiceAccountToken: false` and no Role or RoleBinding;
- `runAsNonRoot`, seccomp `RuntimeDefault`, dropped capabilities, no privilege escalation, and read-only root filesystem;
- one RWO PVC mounted at `/var/lib/alertlens`;
- ClusterIP Service port 9090;
- liveness `/healthz` and readiness `/readyz` probes;
- required CPU/memory requests and limits from values;
- Slack tokens from keys `bot-token` and `app-token` in `slack.existingSecret`;
- `SLACK_ALERT_CHANNELS`, `ALERTMANAGER_URL`, and `HOLMESGPT_URL` from values;
- a default-deny egress NetworkPolicy plus DNS and HTTPS egress. Add explicit CIDR/port egress values for in-cluster Alertmanager and Holmes because Kubernetes NetworkPolicy cannot select a Service by DNS name.

Do not add HPA, PodDisruptionBudget, ingress, autoscaling, external databases, or a values schema.

- [ ] **Step 5: Validate chart rendering**

Run:

```bash
helm lint charts/alertlens --set slack.existingSecret=alertlens-slack --set alertmanagerURL=http://alertmanager:9093 --set holmesURL=http://holmes:5050
helm template alertlens charts/alertlens --set slack.existingSecret=alertlens-slack --set alertmanagerURL=http://alertmanager:9093 --set holmesURL=http://holmes:5050 >/dev/null
```

Expected: both commands exit 0.

If the `helm-unittest` plugin is installed, also run `helm unittest charts/alertlens`; otherwise the CI workflow installs the pinned plugin before running the assertion file.

- [ ] **Step 6: Add CI with the approved gates**

Create `.github/workflows/ci.yaml` triggered by pushes and pull requests. It checks out the repository, installs Go 1.22, Helm 3, and a pinned `helm-unittest` release, then runs:

```bash
test -z "$(gofmt -l .)"
go vet ./...
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | awk '/^total:/ { gsub("%", "", $3); if ($3 + 0 < 90) exit 1 }'
go build ./cmd/alertlens
helm lint charts/alertlens --set slack.existingSecret=alertlens-slack --set alertmanagerURL=http://alertmanager:9093 --set holmesURL=http://holmes:5050
helm unittest charts/alertlens
```

Do not add a separate linter dependency in this milestone; `gofmt` and `go vet` are the requested baseline.

- [ ] **Step 7: Document local startup without publishing secrets**

Update `README.md` with Go 1.22 prerequisites, the five required environment-variable names, `go run ./cmd/alertlens`, health endpoints, test commands, and Helm rendering commands. Use placeholder values and never include a real Slack token or kubeconfig path.

- [ ] **Step 8: Run the complete milestone gate**

Run:

```bash
test -z "$(gofmt -l .)"
go vet ./...
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
go build ./cmd/alertlens
helm lint charts/alertlens --set slack.existingSecret=alertlens-slack --set alertmanagerURL=http://alertmanager:9093 --set holmesURL=http://holmes:5050
helm template alertlens charts/alertlens --set slack.existingSecret=alertlens-slack --set alertmanagerURL=http://alertmanager:9093 --set holmesURL=http://holmes:5050 >/dev/null
```

Expected: every command exits 0 and total statement coverage is at least 90%.

- [ ] **Step 9: Commit the deployable foundation**

```bash
git add Dockerfile .dockerignore .github charts README.md
git commit -m "build(helm): package AlertLens for Kubernetes"
```

---

## Milestone 1 Completion Check

- `go test -race ./...`, `go vet ./...`, `go build ./cmd/alertlens`, Helm lint, and Helm template pass.
- Statement coverage is at least 90%.
- The binary starts with valid placeholder configuration and exposes correct liveness/readiness behavior.
- State survives process restart and corrupt/unwritable state fails safely.
- No Slack connection or real-environment mutation occurs in this milestone.
- Create the milestone 2 plan only after reviewing the concrete foundation APIs; do not pre-plan around interfaces that may change.
