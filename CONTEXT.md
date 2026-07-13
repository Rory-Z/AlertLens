# AlertLens

AlertLens enriches authoritative Alertmanager notifications with investigation context while keeping alert delivery independent of AlertLens.

## Language

**Synthetic Alert**:
An intentionally injected Alertmanager alert that follows normal alert routing but does not represent a real incident.
_Avoid_: Fake alert, test event
