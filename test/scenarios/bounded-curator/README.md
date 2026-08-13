# Bounded deterministic curator acceptance

This provider-free black-box scenario exercises the explicit curator commands
through the public CLI and a real daemon/SQLite database. It creates eleven
owner-accepted structured meeting resolutions and one authenticated agent proposal
whose text, `high` confidence, and `verified` label deliberately look privileged.

The scenario proves that rules start disabled; derive-only processing copies exact
meeting results but leaves them proposed; the queue and idempotent process result
survive a daemon restart; the queue exposes the persisted effective rule revision
before and after enablement; only the exact owner-enabled
`accepted-meeting-resolution-copy` rule can accept those existing revisions; and
one pass accepts no more than ten. A second pass accepts the eleventh while the
agent proposal remains `manual_review` with its real run/task provenance.

A twelfth accepted meeting has a valid 2049-byte proposal summary. Every fresh
process evaluation reports its exact source revision once with
`summary_not_exact_safe_copy`, while creating no truncated knowledge revision,
derivation, authority record, or curator/knowledge fact for it.

It also proves strict fixture input prevents an agent from selecting a source,
the reserved acceptance-tool probe produces only `run.tool_denied`, exact
derivation/authority/event links are inspectable, and retries create no duplicate
derivations, authority decisions, or facts. Store-level mutation-hook tests cover
transaction rollback because no public command exposes failure injection.

The fixture uses the local `fixture-mcp` executable only. It makes no model call,
uses no provider credential, and runs with `GOPROXY=off`; no network access is
required.
