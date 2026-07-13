# AlertLens Slack App Manifest Design

## Goal

Create dev and production Slack Apps from one version-controlled configuration
without allowing their permissions or event subscriptions to drift.

## Artifacts

- `deploy/slack/app-manifest.yaml` is the canonical Slack manifest template.
- `scripts/render-slack-manifest dev|prod` renders importable YAML to stdout.
- `dev` maps the app and bot display names to `AlertLens Dev`.
- `prod` maps the app and bot display names to `AlertLens`.
- Any other environment is rejected without producing a manifest.

The renderer replaces only the fixed app-name placeholder. It does not accept
arbitrary names, tokens, workspace IDs, URLs, or other environment-specific
configuration.

## Slack Configuration

Both rendered manifests enable Socket Mode and define the same:

- bot scopes: `app_mentions:read`, `channels:history`, `groups:history`, `chat:write`,
  `reactions:read`, and `reactions:write`;
- bot events: `app_mention`, `message.channels`, and `message.groups`;
- non-org deployment, non-hosted operation, and disabled token rotation.

No HTTP request URL, interactivity, slash command, user scope, or distribution
configuration is added.

Slack app manifests can enable Socket Mode but cannot issue the required
app-level token. After importing each manifest, an administrator separately
generates an `xapp` token with `connections:write`, installs the app to obtain
its `xoxb` bot token, invites the bot to the monitored channel, and stores both
tokens outside Git.

## Verification

Repository tests render both environments, parse the YAML, assert the expected
display names, and assert that the normalized documents differ only in those
two name fields. The renderer also has rejection coverage for missing or
unknown environments. Existing CI runs these tests through `go test ./...`.
