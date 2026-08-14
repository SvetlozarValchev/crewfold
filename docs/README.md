# Crewfold documentation

This documentation is the product contract for the first implementation. If code
and documentation disagree during the design phase, resolve the disagreement
explicitly rather than silently treating either as authoritative.

## Read in this order

1. [Vision](vision.md) — why Crewfold exists and where its boundary lies.
2. [Product definition](product.md) — users, workflows, requirements, and
   acceptance criteria.
3. [Architecture](architecture.md) — components, process topology, and data flow.
4. [Domain model](domain-model.md) — durable nouns, IDs, states, and invariants.
5. [Coordination model](coordination.md) — delegation, messages, claims, meetings,
   overlap handling, and supervision.
6. [Knowledge system](knowledge.md) — curation, retrieval, context packets, and the
   limited role of RAG.
7. [Context packets and live deltas](context.md) — immutable base packets,
   explicit refresh, run acknowledgement, bounds, and rebase.
8. [Runtime and adapters](runtime-and-adapters.md) — Herdr, provider adapters,
   lifecycle authority, and fallback modes.
9. [Technology stack](stack.md) — proposed implementation choices and rejected
   early complexity.
10. [CLI experience](cli.md) — the intended human-facing command surface.
11. [Implementation plan](implementation-plan.md) — individually demoable,
    testable vertical slices.
12. [Testing strategy](testing.md) — fixtures, failure injection, and acceptance
    gates.
13. [Roadmap](roadmap.md) — compact sequence and release landmarks.
14. [Open questions](open-questions.md) — choices that are deliberately not final.

## References

- [MCP tool contract](reference/mcp-tools.md)
- [Event catalogue](reference/events.md)
- [Implemented local API v1](reference/local-api.md)
- [Implemented storage contract](reference/storage.md)
- [Milestone review template](reference/milestone-review-template.md)
- [Buildable repository milestone review](reviews/buildable-repository.md)
- [Daemon/API spine milestone review](reviews/daemon-api-spine.md)
- [Persistent workspace milestone review](reviews/persistent-workspace.md)
- [Projects/checkouts milestone review](reviews/projects-checkouts.md)
- [Durable coordination milestone review](reviews/durable-coordination.md)
- [Deterministic execution milestone review](reviews/deterministic-execution.md)
- [Direct subprocess milestone review](reviews/direct-runtime.md)
- [Run-scoped MCP milestone review](reviews/scoped-mcp.md)
- [Durable two-agent messaging milestone review](reviews/agent-messaging.md)
- [Herdr runtime milestone review](reviews/herdr-runtime.md)
- [Codex canary implementation audit](reviews/codex-canary.md)
- [Claude canary and provider-switch implementation audit](reviews/claude-canary.md)
- [Claims and overlap milestone review](reviews/claims-overlap.md)
- [Structured meetings milestone review](reviews/structured-meetings.md)
- [Canonical knowledge milestone review](reviews/canonical-knowledge.md)
- [Curation, retrieval, collaboration, and live-context milestone review](reviews/curation-retrieval-live-context.md)
- [Manager proposals and deterministic supervision milestone review](reviews/manager-supervisor.md)
- [Owner-granted local checks milestone review](reviews/local-checks.md)
- [Owner-reviewed outcome briefings milestone review](reviews/outcome-briefings.md)
- [Personal workspace example](examples/personal.yaml)
- [Agent definition example](examples/agent.yaml)

## Architecture decisions

- [ADR-0001: Local-first personal control plane](decisions/0001-local-first-personal-control-plane.md)
- [ADR-0002: Separate durable coordination from runtimes](decisions/0002-separate-coordination-from-runtimes.md)
- [ADR-0003: SQLite event journal and projections](decisions/0003-sqlite-event-journal-and-projections.md)
- [ADR-0004: MCP as the agent-facing coordination surface](decisions/0004-mcp-agent-surface.md)
- [ADR-0005: Curated knowledge over transcript accumulation](decisions/0005-curated-knowledge.md)
- [ADR-0006: Evidence-backed management projections over session summaries](decisions/0006-evidence-backed-management-projections.md)
- [ADR-0007: sqlc typed persistence boundary](decisions/0007-sqlc-typed-persistence-boundary.md)
- [ADR-0008: Exact canonical knowledge revisions](decisions/0008-exact-canonical-knowledge-revisions.md)
- [ADR-0009: Deterministic derived knowledge retrieval](decisions/0009-deterministic-derived-knowledge-retrieval.md)
- [ADR-0010: Participant-bound cross-project collaboration](decisions/0010-participant-bound-cross-project-collaboration.md)
- [ADR-0011: Bounded deterministic context curator](decisions/0011-bounded-deterministic-context-curator.md)
- [ADR-0012: Owner-confirmed exact knowledge contradictions](decisions/0012-owner-confirmed-exact-knowledge-contradictions.md)
- [ADR-0013: Portable project knowledge snapshots](decisions/0013-portable-project-knowledge-snapshots.md)
- [ADR-0014: Explicit bounded live context deltas](decisions/0014-explicit-bounded-live-context-deltas.md)
- [ADR-0015: Owner-granted manager proposals and deterministic supervision](decisions/0015-owner-granted-manager-proposals-and-deterministic-supervision.md)
- [ADR-0016: Owner-granted local checks as fresh mechanical evidence](decisions/0016-owner-granted-local-check-evidence.md)
- [ADR-0017: Owner-reviewed outcomes and bounded management briefings](decisions/0017-owner-reviewed-outcomes-and-bounded-briefings.md)
- [ADR-0018: Go-native operator TUI over the canonical local API](decisions/0018-go-native-operator-tui.md)
- [ADR-0019: Personal-scale hardening and quiescent recovery](decisions/0019-personal-scale-hardening-and-recovery.md)
- [ADR-0020: Local web workbench as the primary owner interface](decisions/0020-local-web-workbench.md)
