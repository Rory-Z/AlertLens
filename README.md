# AlertLens

Alertmanager-first HolmesGPT RCA companion for Slack.

AlertLens is designed as a **sidecar to the existing Alertmanager -> Slack notification path**, not a replacement for it. Alertmanager keeps sending the authoritative alert message to Slack; AlertLens listens to the same alerts, enriches them, asks HolmesGPT for RCA, and posts concise analysis into the matching Slack thread.

See [docs/product-design.md](docs/product-design.md) for the initial product and architecture notes.
