# ADR-0011: Bounded deterministic context curator

- Status: accepted
- Date: 2026-08-13

## Context

Canonical knowledge separates agent proposals from owner-governed acceptance, but
an owner still has to rediscover every structured result that might deserve a
durable decision. Treating the curator as a model with general write authority
would recreate the transcript-noise problem and introduce a new authority bypass:
quality labels, a persuasive body, an agent identity, or a claimed source could
be mistaken for permission to accept knowledge.

The first curator slice must remain useful without a provider, model, network, or
background scheduler. It must survive restart, be safe to retry, expose why an
item is waiting or accepted, and process a bounded amount of work. Its queue also
must not become a second canonical source of truth.

## Decision

The curator queue is a read projection over canonical proposed knowledge
revisions plus immutable, append-only derivation records. It has no stored queue
table and no independently editable body or review state. Canonical knowledge
revisions, governance records, derivations, and the event journal remain the
authoritative records from which a queue response is produced. Every response
also contains the effective immutable rule snapshot so its enabled state and
optimistic revision remain observable after restart. The rule, revision entries,
and eligibility classifications come from one read transaction. Every entry is
scoped to one workspace and project and names the exact `krev_...` revision it
represents.

`curator process` is an explicit owner-local command, not a daemon background loop.
Each pass observes at most 100 candidate inputs in deterministic order and may
auto-accept at most ten revisions. With the explicit `--apply-safe` opt-in and an
enabled rule, it evaluates already-derived safe proposals first so repeatable
invalid-source evaluations cannot starve governance progress. It uses the
remaining candidate budget to derive missing proposals, prioritizing exact safe
sources before invalid-source skip evaluations. When acceptance capacity remains,
a newly derived exact proposal is revalidated and accepted in that same opted-in
pass. A derive-only pass omits the flag. Repeating a pass, including after restart,
reuses the exact derivation and knowledge revision rather than creating a
duplicate. A failed transaction publishes neither a partial derivation nor a
partial governance decision; retry reevaluates current canonical state.

Every workspace persists the rule in disabled revision one by default. This slice
defines exactly one public rule alias,
`accepted-meeting-resolution-copy`, stored as
`accepted_meeting_resolution_copy/v1`. Its derivation is intentionally
mechanical:

- the source meeting is concluded and its proposal is accepted;
- the frozen meeting agenda is valid UTF-8 from one through 160 bytes and the
  proposal summary is valid UTF-8 from one through 2 KiB;
- the knowledge type is `decision`;
- the title is exactly the frozen meeting agenda and the body is exactly the
  accepted proposal summary;
- applicability is project-wide;
- confidence is `medium`, verification is `supported`, and freshness is
  `until_superseded`;
- provenance is one primary `meeting_proposal` source at the proposal's exact
  accepted revision; and
- there is no task scope, supporting source, predecessor, successor selection, or
  caller-supplied content.

The transform never truncates or repairs source text. An otherwise accepted
meeting whose exact agenda or summary exceeds those bounds is skipped without a
derivation and reports bounded source identity plus a stable ineligibility reason
in that process result. A later pass may report the skip again because it is an
evaluation result, not a durable proposal, queue entry, or fact event.
In particular, a valid accepted proposal summary above 2 KiB reports
`summary_not_exact_safe_copy` and produces no truncated output.

Processing while the rule is disabled still creates that exact proposed revision
and leaves it awaiting owner review. Enabling the rule with an optimistic policy
revision and processing again with `--apply-safe` may accept that same revision.
The rule is not an inference that the meeting is true in every context; it is a
narrow policy that copies an already accepted structured resolution into reusable
project knowledge.

The auto-accept path is a distinct internal governance operation. In the same
transaction it revalidates the enabled rule, exact derivation identity and hashes,
source meeting/proposal state and revisions, and the still-proposed knowledge
state. A successful authority check records actor `subsystem:curator`, action
`accept`, outcome `allowed`, and reason `state_policy`, linked to the exact
derivation and rule evaluation, then appends the ordinary `knowledge.accepted`
fact. The public knowledge-accept operation does not gain subsystem authority:
direct acceptance by an agent, another subsystem, or even a caller claiming to be
the curator remains denied and preserves the proposal.

Agent-created proposals—regardless of `high` confidence, `verified` status, text,
or forged-looking source claims—remain queued for owner review. No other source or
rule can auto-accept in this slice. Rule enable/disable is owner-only, durable,
optimistically versioned, and auditable. Processing does not search transcripts,
call a model, use FTS rank as authority, or ingest collaboration messages.

## Consequences

- The owner gets one inspectable queue instead of reconstructing possible durable
  knowledge from individual sessions.
- A queue response can be recomputed from canonical proposal/governance state and
  retained immutable derivations. Derivations themselves are append-only
  provenance and are not discarded or inferred from knowledge revisions alone.
- One safe structured result can become reusable knowledge with deterministic,
  bounded work and exact authority evidence.
- Labels and content never grant acceptance authority, and adding a future rule
  requires a new explicit transform and authority contract.
- Background scheduling, model-assisted summarization, handoff/review/test rules,
  contradictions, context deltas, and broader knowledge types remain separate
  work.
