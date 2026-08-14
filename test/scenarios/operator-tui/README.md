# Operator TUI acceptance

This provider-free M19 scenario exercises the built `crewfold ui` command through
a real pseudo-terminal against an isolated daemon and deterministic canonical
records. It proves the live project dashboard renders the complete canonical M18
briefing cursor, cutoff, and SHA-256, navigation and ordinary attach append no events, the recorded
Herdr child suspends and returns to Bubble Tea, daemon loss shows retained state
as stale, restart resynchronizes without relaunching the UI, the selected run is
retained, and one reviewed Ctrl+Enter resume creates exactly one `run.resumed`
event. The terminal is explicitly monochrome and 180x40.

The scenario uses no provider credentials, network access, direct SQLite reads,
second renderer, or hidden product mode. Canonical API results and journal events
remain the authority; the captured terminal frames prove only what the operator
surface displayed and which keyboard path it executed.
