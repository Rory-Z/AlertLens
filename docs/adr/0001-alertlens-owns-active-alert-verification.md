# AlertLens owns active-alert verification

AlertLens verifies that Alertmanager has a current active alert matching Alert Identity before calling Holmes; Holmes then owns the RCA using the verified snapshot and its investigation tools. This keeps the gate deterministic and its failures observable, while avoiding dependence on an LLM-selected HTTP tool that the current Holmes API does not expose as a fail-closed verification contract; see the [supporting research](../research/2026-07-14-holmes-alertmanager-api.md).
