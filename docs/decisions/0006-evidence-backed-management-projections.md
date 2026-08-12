# ADR-0006: Evidence-backed management projections over session summaries

- Status: accepted
- Date: 2026-08-12

## Context

With several mostly autonomous coding agents, implementation volume can exceed
what one owner can personally inspect. Polling sessions, concatenating completion
messages, or asking another model to summarize transcripts creates a second wall
of prose. It also mixes activity with delivery, self-report with independent
evidence, and unresolved disagreement with accepted truth.

Crewfold needs to support management at the level of deliverables, reliability,
stability, rationale, residual risk, and next decisions while retaining a path to
the underlying evidence.

## Decision

Make management comprehension a first-class product contract. Represent promised
deliverables and revisioned outcome assessments explicitly. Derive bounded
management briefings from structured outcome, decision, evidence, check, risk,
overlap, and follow-up records at a declared event cursor and optional owner
checkpoint.

Roll these records up through task, objective, project, and workspace scopes. Every
material aggregate claim retains provenance, authority, evidence strength,
freshness, and conflict state. Activity and run completion do not imply accepted
delivery.

Keep the deterministic structured projection available without a model. A model
may render it for an audience or propose next actions, but cannot add facts,
silently resolve contradictions, or become the only stored representation.
Provider transcripts remain optional drill-down evidence and are not required for
the management view.

Assessment review state is separate from its conclusion: an authorized reviewer
can accept the finding that a deliverable is partial, not achieved, or still
unknown without that acceptance being misread as successful delivery.

## Consequences

- The owner can reason about more work than they can inspect line by line.
- Briefings remain explainable and can be compared across checkpoints.
- Managers and deeper hierarchies can compress by scope without concatenating
  subordinate prose.
- Crewfold must model acceptance, evidence quality, risks, and unknowns instead of
  relying on generic task status.
- Agents and reviewers must emit more structured reports, which adds workflow and
  schema cost.
- The system can describe evidence strength and residual uncertainty, but cannot
  promise that unobserved defects do not exist.

## Rejected alternatives

- Treat a terminal dashboard as sufficient management understanding.
- Store agent-authored Markdown summaries as the project record.
- Use transcript RAG as the canonical explanation of delivery.
- Infer outcomes from commits, line counts, run completion, or passing checks
  alone.
