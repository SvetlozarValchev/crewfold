# ADR-0005: Curated knowledge over transcript accumulation

- Status: accepted
- Date: 2026-08-12

## Context

Sharing every agent transcript appears simple but scales poorly, mixes speculation
with decisions, leaks unrelated data, and consumes context budgets. Agents still
need current project constraints, prior findings, decisions, and handoffs.

## Decision

Separate raw observations, evidence, proposed knowledge, and accepted knowledge.
Use versioned scoped records with provenance, authority, confidence, freshness,
and supersession. Assemble immutable task-specific context packets using structured
links, deterministic filters, full-text search, and explicit budgets.

Use a curator to propose concise updates. Require appropriate authority for
decisions and constraints. Treat semantic retrieval as optional candidate discovery,
not a source of truth.

## Consequences

- Context remains explainable and compact.
- Important knowledge requires curation and governance work.
- Full provider transcripts are not available by default as global memory.
- Contradictions and staleness become first-class states.
- RAG and embeddings can evolve without changing canonical knowledge semantics.
