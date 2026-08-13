# Live context delta acceptance

This provider-free black-box scenario proves the current context-packet and live-delta contract
through the built Crewfold binary. It uses temporary Git repositories, one
isolated SQLite database, public CLI/local API operations, and the checked-in
`fixture-mcp` consumer. It makes no model call, opens no network connection, uses
no credential, and configures no Git remote.

The scenario freezes direct/reverse dependency and participant-roster context,
accepts a task-scoped decision only after its run starts, and requires an explicit
owner refresh to build one delta. It preserves that pending object across daemon
restart, then proves exact-run fetch/acknowledgement and idempotent receipt replay.
Later deltas carry a bounded cross-project message preview and full roster, a new
reverse dependent, contradiction opening plus knowledge withdrawal, and
contradiction closure. Another task of the same agent receives none of them and
cannot acknowledge the first run's delta.

The dispute delta withdraws both previously delivered decisions. The closure
delta separately carries the closed contradiction and re-offers both decisions as
`knowledge_accepted` with cause `contradiction_closed_reoffer`; this proves the
base stays immutable while the effective live chain regains eligible knowledge.

The final no-op refresh advances the inspected cursor without appending an event
or empty delta. Local list/show/explain and build/ack/rebase journal counts remain
inspectable across daemon restart without inventing state or an event.

Per-delta, cumulative-chain, event-window, strict protocol, trigger, and rollback
bounds are covered by the store/protocol component tests in the same offline gate.
