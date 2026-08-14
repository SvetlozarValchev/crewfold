# Milestone review — outcome ledger and management briefings

## Identity

- Milestone: `M18 — Outcome ledger and management briefings`
- Review status: `passed`
- Implementation commit: `2e7ee9882173a91a7997dead40aaccd091fcb901`
- Reviewer: `automated acceptance and independent adversarial review`
- Date: `2026-08-14`

## Demonstrable outcome

Crewfold now lets the local owner freeze an explicit deliverable commitment before
work, record a structured judgment of what was achieved, and separately accept or
reject that judgment. A completed run, handoff, check, or proposed assessment is
never accepted delivery by implication. Accepted `partial`, `not_achieved`, and
`unknown` conclusions remain honest current outcomes rather than being coerced
into success.

The owner can create an exact task, objective, project, or workspace checkpoint
and derive a bounded management briefing at the current workspace high-water.
The briefing answers what was promised, delivered, changed, verified, disputed,
still risky or unknown, and what needs owner attention without reading provider
transcripts. Every material claim retains exact structured provenance and can be
explained through the owner-local API and CLI.

## Outcome contract and authority proof

- An immutable owner-created `DeliverableCommitment` freezes exact task scope,
  ordered acceptance criteria, and canonical content before an assessment can
  cite it. Commitment keys are unique per task and have no mutation lifecycle.
- Assessments separate review state `proposed|accepted|rejected|superseded` from
  conclusion `achieved|partial|not_achieved|unknown`. At most one proposal and
  one current accepted assessment exist for a commitment.
- A successor must cite the exact current accepted assessment. Its acceptance
  atomically supersedes that assessment and accepts the successor; rejection
  leaves the current accepted assessment unchanged.
- Assessment content is immutable and structurally complete. Typed, ordered,
  bounded children retain decision revisions, evidence sources, effects,
  deviations, risks, unknowns, follow-up tasks, and owner-attention items.
- Caller evidence is closed to an exact `handoff` or
  `check_requirement_evidence`. Crewfold resolves the source and derives its
  class, effect, verification strength, pinned freshness, current freshness,
  dispute state, current truth, and diagnosis. Exact accepted independent-review
  request provenance is required before evidence can be classified that way.
- Acceptance basis is derived from the exact policy and durable evidence at the
  owner decision; callers cannot assert or upgrade it. Only `local-owner` can
  create commitments, propose assessments, and accept or reject them.
  `AgentDefinition.Role` and `LaunchProfile.Purpose` remain inert descriptive
  metadata and grant no outcome authority.
- The single current surface is the owner-local API and CLI. Proposal documents
  use strict JSON or YAML with the exact `{commitment, assessment}` wrapper.
  There is no agent/run-scoped outcome MCP mutation surface.

## Briefing, provenance, and bounds proof

- Checkpoints are immutable exact-scope records of the event cursor, creation
  time, and owner identity. They have no archive state. A briefing always captures
  the current workspace high-water and may use one same-scope checkpoint only as
  an exclusive lower bound; callers cannot select a historical cursor.
- The projector consumes a closed event union in restart-safe pages of at most
  1000 records. Unknown event kinds fail closed. Projector state advances only
  with the page it has fully interpreted.
- Change and history claims are strictly newer than the checkpoint, while older
  unresolved current facts remain visible. Superseded exceptions cease to appear
  as current facts but remain traceable as history.
- Material claim IDs are stable hashes of their semantic scope, kind, ordered
  sources, and status. Explanation returns exact entity revisions, hashes, event
  sequences, evidence classification, effect, pinned freshness, current
  freshness, and current diagnosis.
- One briefing contains at most 128 whole claims and 64 KiB of canonical JSON.
  Closed section quotas and ordering prevent one category from consuming the
  view; workspace selection applies urgency bands followed by round-robin project
  fairness. Canonical omission counts identify section and
  `claim_limit|byte_limit` reason.
- Expiry is evaluated from real RFC 3339 timestamps, including offsets and
  fractional seconds. Briefing materialization, display, and explanation append
  no fact event and grant no authority.
- The bounded structured projection is the complete representation. There is no
  alternative narrative renderer or model-generated correctness path.

## Recovery and security proof

- Outcome mutation transactions serialize commitment streams and atomically
  persist domain rows, typed children, receipts, events, acceptance basis, and
  idempotency responses. Concurrent proposals and governance decisions resolve
  to one legal current state rather than producing duplicate or partial effects.
- Named fault barriers cover commitment creation, proposal, acceptance,
  rejection, successor supersession, checkpoint creation, event-page projection,
  and briefing materialization. Restart after a committed response boundary
  returns the one durable assessment and the same legal current outcome.
- Current-schema triggers reject direct construction, scope and provenance
  mismatches, noncanonical content, child ordinal gaps, illegal governance
  transitions, mutation of immutable records, detachment, and deletion. Read
  validation also fails closed when storage is corrupted beneath the Store.
- Adversarial tests cover raw-SQL construction and mutation, exact decision and
  evidence scope, independent-review forgery, evidence upgrades, timestamp
  expiry, receipt parity, more-than-1000-event restart, unknown events, projector
  fault barriers, concurrent mutation, cap pressure, project fairness, and
  stable provenance.
- Outcome persistence uses named `sqlc` queries and one current baseline schema.
  The implementation contains no schema-version ladder, upgrade fixture path,
  compatibility shim, alternate outcome representation, or transitional stub.

## Public acceptance

The provider-free `test/scenarios/outcome-briefings/run.sh` scenario creates ten
transcript-free task histories and an immutable pre-work commitment for each.
Three real fixture runs produce handoffs, but only explicit owner acceptance
creates accepted delivery. The scenario exercises accepted achieved and partial
judgments, a proposal that remains unaccepted, a rejected unknown judgment, and
an accepted successor that atomically supersedes the old current judgment.

After an exact project checkpoint and daemon restart, public CLI reads recover
both sides of the successor transition. The checkpoint briefing includes each
new change once, retains older current accepted delivery, risks, and unknowns,
removes superseded exceptions from current state, stays within 128 claims and
64 KiB, and explains a stable claim through exact provenance. Comparing the event
ledger before and after `briefing show` and `briefing explain` proves both reads
are authority-free and event-free.

The reviewed tree passed:

- generated database parity, formatting, `go vet ./...`, and `go test ./...`;
- `go test -race -timeout 20m ./...`, including the Store suite in 631.181
  seconds, with the explicit repository-scale timeout replacing Go's now-too-small
  default package deadline;
- every public black-box scenario in `scripts/check.sh`, including local checks,
  outcome briefings, Herdr, Codex, and Claude;
- focused domain, local API, CLI, daemon, and protocol outcome tests;
- independent raw-SQL, trust, construction-completeness, corruption, concurrency,
  scope, time, cap/fairness, projector, and fault-barrier audits with no unresolved
  high- or medium-severity finding.

## Deferrals

The M19 terminal dashboard, live process attachment, and operator intervention;
M20 personal-scale load, backup, restore, repair, and endurance work; and M21
packaging and public-release readiness remain outside M18. Historical-cursor
reads, checkpoint archives, agent-authored outcome mutation, caller-supplied
evidence classification, an outcome MCP surface, and an alternate narrative
briefing are deliberately absent from the current product contract rather than
dormant paths.
