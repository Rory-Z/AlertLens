# Slack App Manifest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render dev and production AlertLens Slack App manifests from one canonical YAML configuration.

**Architecture:** Keep all Slack settings in one YAML template and replace one fixed app-name placeholder through a POSIX shell script. A root Go contract test invokes the renderer, parses both outputs with the YAML package already present in `go.mod`, and proves the normalized documents are identical.

**Tech Stack:** Slack App Manifest YAML v1, POSIX shell, Go 1.25 tests, `go.yaml.in/yaml/v2`.

## Global Constraints

- `dev` renders app and bot display names as `AlertLens Dev`.
- `prod` renders app and bot display names as `AlertLens`.
- Any other or missing environment exits non-zero without a manifest.
- Both environments have identical scopes, events, and Socket Mode settings.
- Tokens, workspace IDs, channel IDs, request URLs, and interactivity are absent.
- App-level `xapp` tokens remain a manual Slack administration step.

---

### Task 1: Canonical Slack manifest and renderer

**Files:**
- Create: `deploy/slack/app-manifest.yaml`
- Create: `scripts/render-slack-manifest`
- Create: `slack_manifest_test.go`
- Modify: `README.md`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: one command argument, exactly `dev` or `prod`.
- Produces: an importable Slack App Manifest YAML document on stdout.

- [ ] **Step 1: Write the failing renderer contract test**

Create `slack_manifest_test.go`:

```go
package alertlens_test

import (
	"bytes"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

type slackManifest struct {
	Metadata struct {
		MajorVersion int `yaml:"major_version"`
	} `yaml:"_metadata"`
	DisplayInformation struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	} `yaml:"display_information"`
	Features struct {
		BotUser struct {
			DisplayName  string `yaml:"display_name"`
			AlwaysOnline bool   `yaml:"always_online"`
		} `yaml:"bot_user"`
	} `yaml:"features"`
	OAuthConfig struct {
		Scopes struct {
			Bot []string `yaml:"bot"`
		} `yaml:"scopes"`
	} `yaml:"oauth_config"`
	Settings struct {
		EventSubscriptions struct {
			BotEvents []string `yaml:"bot_events"`
		} `yaml:"event_subscriptions"`
		OrgDeployEnabled    bool `yaml:"org_deploy_enabled"`
		SocketModeEnabled   bool `yaml:"socket_mode_enabled"`
		IsHosted            bool `yaml:"is_hosted"`
		TokenRotationEnabled bool `yaml:"token_rotation_enabled"`
	} `yaml:"settings"`
}

func TestSlackManifestEnvironments(t *testing.T) {
	wantNames := map[string]string{"dev": "AlertLens Dev", "prod": "AlertLens"}
	wantScopes := []string{
		"app_mentions:read", "channels:history", "chat:write",
		"reactions:read", "reactions:write",
	}
	wantEvents := []string{"app_mention", "message.channels"}
	var normalized *slackManifest

	for environment, wantName := range wantNames {
		t.Run(environment, func(t *testing.T) {
			output, err := exec.Command("./scripts/render-slack-manifest", environment).Output()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(output), "__ALERTLENS_APP_NAME__") {
				t.Fatalf("unrendered placeholder in %q", output)
			}
			var got slackManifest
			if err := yaml.Unmarshal(output, &got); err != nil {
				t.Fatal(err)
			}
			if got.Metadata.MajorVersion != 1 ||
				got.DisplayInformation.Name != wantName ||
				got.Features.BotUser.DisplayName != wantName ||
				!reflect.DeepEqual(got.OAuthConfig.Scopes.Bot, wantScopes) ||
				!reflect.DeepEqual(got.Settings.EventSubscriptions.BotEvents, wantEvents) ||
				!got.Settings.SocketModeEnabled || got.Settings.OrgDeployEnabled ||
				got.Settings.IsHosted || got.Settings.TokenRotationEnabled {
				t.Fatalf("manifest = %#v", got)
			}

			got.DisplayInformation.Name = ""
			got.Features.BotUser.DisplayName = ""
			if normalized == nil {
				normalized = &got
			} else if !reflect.DeepEqual(*normalized, got) {
				t.Fatalf("environment manifests differ beyond names: %#v != %#v", *normalized, got)
			}
		})
	}
}

func TestSlackManifestRejectsUnknownEnvironment(t *testing.T) {
	for _, arguments := range [][]string{nil, {"staging"}} {
		var stdout bytes.Buffer
		command := exec.Command("./scripts/render-slack-manifest", arguments...)
		command.Stdout = &stdout
		if err := command.Run(); err == nil || stdout.Len() != 0 {
			t.Fatalf("arguments = %v, error = %v, stdout = %q", arguments, err, stdout.String())
		}
	}
}
```

The expected contracts are:

```go
wantNames := map[string]string{"dev": "AlertLens Dev", "prod": "AlertLens"}
wantScopes := []string{
	"app_mentions:read", "channels:history", "chat:write",
	"reactions:read", "reactions:write",
}
wantEvents := []string{"app_mention", "message.channels"}
```

- [ ] **Step 2: Run the contract test and verify it fails**

Run:

```bash
go test . -run TestSlackManifest -v
```

Expected: FAIL because `scripts/render-slack-manifest` does not exist.

- [ ] **Step 3: Add the canonical manifest**

Create `deploy/slack/app-manifest.yaml`:

```yaml
_metadata:
  major_version: 1
display_information:
  name: __ALERTLENS_APP_NAME__
  description: Alertmanager-first HolmesGPT RCA companion
features:
  bot_user:
    display_name: __ALERTLENS_APP_NAME__
    always_online: false
oauth_config:
  scopes:
    bot:
      - app_mentions:read
      - channels:history
      - chat:write
      - reactions:read
      - reactions:write
settings:
  event_subscriptions:
    bot_events:
      - app_mention
      - message.channels
  org_deploy_enabled: false
  socket_mode_enabled: true
  is_hosted: false
  token_rotation_enabled: false
```

- [ ] **Step 4: Add the fixed renderer**

Create executable `scripts/render-slack-manifest`:

```sh
#!/bin/sh
set -eu

case "${1:-}" in
  dev) app_name='AlertLens Dev' ;;
  prod) app_name='AlertLens' ;;
  *)
    echo 'usage: scripts/render-slack-manifest dev|prod' >&2
    exit 2
    ;;
esac

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
sed "s/__ALERTLENS_APP_NAME__/$app_name/g" "$repo_root/deploy/slack/app-manifest.yaml"
```

Set mode `0755`. Run `go mod tidy` so the already-present
`go.yaml.in/yaml/v2` dependency is recorded as direct test usage.

- [ ] **Step 5: Run the contract and full tests**

Run:

```bash
go test . -run TestSlackManifest -v
go test -race -coverprofile=coverage.out ./...
```

Expected: all tests PASS and total statement coverage remains at least 90%.

- [ ] **Step 6: Document creation and manual token steps**

Update `README.md` under `Slack app` with:

```bash
./scripts/render-slack-manifest dev  > /tmp/alertlens-dev.yaml
./scripts/render-slack-manifest prod > /tmp/alertlens-prod.yaml
```

Document importing the chosen file in Slack, generating a separate app-level
token with `connections:write`, installing the app for the `xoxb` token,
inviting it to the monitored channel, and storing both tokens in the existing
Kubernetes Secret keys `app-token` and `bot-token`.

- [ ] **Step 7: Run repository gates and commit**

Run:

```bash
test -z "$(gofmt -l .)"
go vet ./...
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | awk '/^total:/ { gsub("%", "", $3); if ($3 + 0 < 90) exit 1 }'
go build ./cmd/alertlens
helm lint charts/alertlens \
  --set slack.existingSecret=alertlens-slack \
  --set-string 'slack.alertChannels[0]=C1' \
  --set alertmanagerURL=http://alertmanager:9093 \
  --set holmesURL=http://holmes:5050
helm unittest charts/alertlens
```

Expected: all commands PASS.

Commit:

```bash
git add deploy/slack/app-manifest.yaml scripts/render-slack-manifest \
  slack_manifest_test.go README.md go.mod go.sum \
  docs/superpowers/plans/2026-07-13-slack-app-manifest.md
git commit -m "feat(slack): add reproducible app manifest"
```
