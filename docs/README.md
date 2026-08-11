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
7. [Runtime and adapters](runtime-and-adapters.md) — Herdr, provider adapters,
   lifecycle authority, and fallback modes.
8. [Technology stack](stack.md) — proposed implementation choices and rejected
   early complexity.
9. [CLI experience](cli.md) — the intended human-facing command surface.
10. [Roadmap](roadmap.md) — incremental delivery plan.
11. [Open questions](open-questions.md) — choices that are deliberately not final.

## References

- [MCP tool contract](reference/mcp-tools.md)
- [Event catalogue](reference/events.md)
- [Personal workspace example](examples/personal.yaml)
- [Agent definition example](examples/agent.yaml)

## Architecture decisions

- [ADR-0001: Local-first personal control plane](decisions/0001-local-first-personal-control-plane.md)
- [ADR-0002: Separate durable coordination from runtimes](decisions/0002-separate-coordination-from-runtimes.md)
- [ADR-0003: SQLite event journal and projections](decisions/0003-sqlite-event-journal-and-projections.md)
- [ADR-0004: MCP as the agent-facing coordination surface](decisions/0004-mcp-agent-surface.md)
- [ADR-0005: Curated knowledge over transcript accumulation](decisions/0005-curated-knowledge.md)
