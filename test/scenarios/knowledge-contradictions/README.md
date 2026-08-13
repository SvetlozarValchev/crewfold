# Owner-confirmed knowledge contradiction acceptance

This provider-free black-box scenario runs a real daemon and SQLite database
through the public CLI. It renders a strict fixture template only after the two
exact knowledge revision IDs exist, then lets an authenticated `fixture-mcp` run
report the pair. The fixture proves canonical reversed-pair replay and that the
reserved confirmation probe is denied without reaching knowledge governance.

The scenario proves a proposed report has no retrieval effect; owner confirmation
quarantines both exact participants without changing knowledge currency; a
project-wide participant stays quarantined in project-only and unrelated-task
reads even when its peer is task-scoped; search applies the relational exclusion
before `LIMIT`; an otherwise eligible explicit context build fails atomically;
and the pre-confirmation packet remains immutable. Dismissal restores search and
context eligibility, the failed context key can be retried, the decision replay
and records survive restart, and the globally unique exact pair cannot be
reported again.

The MCP-input sentinel is stored only as the canonical report reason and is
explicitly absent from captured provider logs. The scenario makes no model call,
uses no provider credential, runs with `GOPROXY=off`, and needs no network.
