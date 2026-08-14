# ADR-0017: Owner-reviewed outcomes and bounded management briefings

- Status: accepted
- Date: 2026-08-14

## Context

Crewfold records tasks, runs, handoffs, decisions, messages, local checks, and
other evidence, but activity is not the same thing as delivered value. A
completed run or passing command cannot say which promised deliverable was
achieved, whether an incomplete result was accepted as partial, or which risks
still need the owner. Reading provider transcripts does not scale to a project
with many concurrent task histories and would make provider prose an accidental
source of truth.

The missing boundary is an explicit, owner-reviewed outcome ledger and a
deterministic projection over its durable facts. Assessment input must retain
exact links to Crewfold records without letting a caller upgrade self-report to
mechanical or independent verification. A project briefing must remain bounded,
stable, and explainable even when the underlying project is larger than its
output budget.

## Decision

### Commitments precede assessments

The local owner creates a `DeliverableCommitment` for one exact task and its
objective before an outcome can be assessed. A commitment is durable scope, not
a task completion claim. It records the promised deliverable independently from
run activity so work cannot acquire a delivery assertion merely by finishing.

M18 has one current owner-local mutation path. Agents, run-scoped MCP tools,
watchers, checks, roles, and launch-profile purpose strings cannot create a
commitment, propose or decide an assessment, or publish a briefing claim.

### Assessments use an owner-reviewed revision stream

An `OutcomeAssessment` evaluates one exact commitment. Its review state is
closed to `proposed`, `accepted`, `rejected`, and `superseded`; its conclusion is
separately closed to `achieved`, `partial`, `not_achieved`, and `unknown`.
Accepting a partial, unsuccessful, or unknown conclusion therefore records an
honest owner judgment without pretending success.

Only the local owner proposes, accepts, or rejects an assessment. Mutations use
idempotency keys and expected revisions. A later proposal is a successor in the
same commitment stream. Accepting it atomically makes it current and supersedes
the prior accepted assessment, retaining the full immutable history. A rejected
proposal never becomes current. There is no agent proposal authority, policy
shortcut, implicit acceptance from task/run/check state, or compatibility alias.

Each proposal has typed exact links for decisions, evidence, effects,
deviations, risks, unknowns, follow-up tasks, and owner-attention records. The
caller identifies source records; the Store resolves their canonical scope and
derives evidence class, freshness, and current truth. A caller cannot label its
own evidence mechanical, independent, fresh, accepted, or authoritative.
Caller evidence is closed to `handoff` and `check_requirement_evidence`.
Independent review is derived only from an accepted review-task handoff whose
exact request provenance is `request_review`; policy acceptance is derived only
from accepted outcome governance and is never assessment input.

### Owner checkpoints are immutable cursors

The owner may create an immutable checkpoint at exactly one task, objective,
project, or workspace scope. It freezes the scope, creation time, and event
cursor. Checkpoints are never edited or archived. A checkpoint does not copy or
summarize outcome data and does not change any assessment.

A briefing may use one same-scope checkpoint as an exclusive lower event bound.
Facts at or before the checkpoint cursor are excluded from the change view.
Scope mismatch is an error rather than a best-effort comparison.

### Briefings are one deterministic structured projection

A management briefing is a read-only derived projection over current durable
commitments, accepted assessments, decisions, evidence and freshness, effects,
deviations, risks, unknowns, follow-up work, and owner attention. It declares
its exact scope, as-of event cursor, optional base checkpoint, and deterministic
omission counts.

One briefing contains at most 128 whole material claims and at most 64 KiB of
canonical JSON. Claims are ordered deterministically. The projector never emits
a partial claim to fit the budget; omitted facts are counted by their typed
section. Querying, explaining, or encoding a briefing emits no event.

Section admission is fixed: required decisions, contradictions, risks and
unknowns, verification gaps, deviations and unmet commitments, accepted
delivery, then rationale/change history. Required quotas are reserved before
lower-priority admission. Project and workspace briefings use stable round-robin
selection across projects so one busy project cannot consume the entire budget.
The projector consumes only a closed event union in pages of at most 1000.
Outcome mutations, projector cursor pages, and briefing materialization commit in
separate transactions, making replay restart-safe; an unknown event type fails
closed with an explicit diagnostic.

Every material claim has a stable ID and typed exact provenance to its durable
source records and source event sequences. `briefing explain` resolves that
claim within its exact briefing scope and cursor, returning the structured claim
and provenance rather than model-authored rationale. There is no narrative
renderer, degraded-rendering mode, prose cache, or renderer failure branch.
The ID is `bclaim_` plus the lowercase SHA-256 of canonical scope, semantic kind,
exact ordered sources, and status.

`briefing show` always captures the current workspace high-water. A caller may
name only an exact same-scope checkpoint as an exclusive lower bound; the public
surface has no caller-selected historical cursor.

### Public protocol and fact set are closed

The current owner-local API and CLI expose only:

- `outcome commitment add`;
- `outcome propose`, `show`, `list`, `accept`, and `reject`;
- `checkpoint create`, `show`, and `list`; and
- `briefing show` and `explain`.

The durable events are exactly:

```text
outcome.commitment_created
outcome.assessment_proposed
outcome.assessment_accepted
outcome.assessment_rejected
outcome.assessment_superseded
owner_checkpoint.created
```

Briefing reads append no event. The protocol publishes one current schema for
each request, domain record, and result. It contains no deprecated names,
version ladders, checkpoint archive operation, agent/MCP outcome tool, optional
renderer, or inert future stub.

## Consequences

- The owner can distinguish work performed from delivery accepted, including an
  accepted partial, unsuccessful, or unknown result.
- Accepted delivery always traces through one explicit commitment and one owner
  decision; passing checks and completed runs remain supporting evidence only.
- Evidence classifications and freshness cannot be inflated by request input.
- A new accepted revision replaces the current assessment atomically while
  preserving the prior accepted judgment as superseded history.
- Immutable cursor checkpoints make change views repeatable without copying
  project state.
- Briefings remain bounded without silently truncating a claim, and every visible
  claim can be drilled into exact durable provenance.
- Structured output is the complete product boundary; operation never depends on
  a model, provider transcript, or narrative-rendering service.

## Rejected alternatives

- Infer delivery from a completed run, handoff, task transition, or passing
  check: activity and evidence do not supply owner acceptance.
- Let agents or a role label propose outcomes: M18 is deliberately owner-only
  and exposes no run-scoped outcome capability.
- Let callers select evidence strength or freshness: those are canonical derived
  facts, not assessment rhetoric.
- Mutate the prior assessment in place: immutable revision history is required
  to explain changed judgment.
- Archive checkpoints: immutable cursors need no lifecycle state.
- Truncate arbitrary JSON bytes or source arrays: a briefing includes whole
  claims and reports deterministic omissions.
- Add an optional narrative renderer: a second representation adds failure,
  drift, and dead abstraction without changing the structured facts the owner
  must be able to trust.
