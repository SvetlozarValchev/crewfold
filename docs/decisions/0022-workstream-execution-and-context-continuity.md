# ADR-0022: Workstream execution homes and durable context continuity

- Status: accepted
- Date: 2026-08-19
- Supersedes: the resource-neutral workstream and status-only dependency context
  portions of [ADR-0021](0021-domain-oriented-durable-agent-console.md)

## Context

ADR-0021 corrected Crewfold's owner model from one checkout-bound executive to a
domain containing several workstreams and an arbitrary durable-agent hierarchy.
Real use of that implementation exposed three composition failures.

First, a workstream is currently only an Objective. The scheduler chooses any
eligible checkout in the Project unless an owner-created launch profile happens
to pin one. That is technically flexible but loses the physical context of real
local development. A warmed checkout may contain installed dependencies, large
build trees, generated state, cooked assets, local fixtures, and hours of useful
preparation. Starting a new process must not imply creating a new clone or cold
environment.

Second, the initial coordinator flow created durable specialists before their
Objective existed. Proposal acceptance created the objective, tasks,
dependencies, and scheduling intents but did not place those specialists into
the resulting workstream. The accepted graph could therefore run while the
owner-visible workstream said it had no agents, and rejecting the graph still
left an unwanted team behind.

Third, a dependency currently contributes only its task ID, title, status, and
revision to a successor context packet. A completed reviewer can record a handoff,
findings, checks, and evidence while a remediation successor receives only
`review: completed`. The successor then either guesses or blocks despite the
required canonical input already existing. A generic `resume` control cannot
repair an immutable packet that omitted the input.

These failures share one cause: Crewfold records individually correct entities
without closing the workstream's organization, execution environment, and
information flow into one usable contract.

## Decision

### A workstream has one primary persistent checkout

An Objective presented as a workstream has at most one current
`primary_checkout_id`. It may additionally name bounded reference checkouts.

- A workstream containing source-mutating implementation, remediation,
  integration, or repair work must have one available writable primary checkout
  before that work can be accepted or scheduled.
- A coordination-only objective may omit a primary checkout.
- Reference checkouts are read-only unless an exact task/run override separately
  grants mutation authority.
- Creating a run reuses the selected concrete directory. It does not clone,
  relocate, initialize, install dependencies, clear caches, or rebuild merely
  because a new process or provider epoch starts.
- Workstream membership supplies default context and placement, not write
  authority. Mutation still requires an assigned task, current launch profile,
  checkout capacity, claims/policy, and one exact run capability.
- The primary checkout is immutable for the lifetime of the M23 workstream. A
  changed physical home is represented by closing the old workstream and
  reviewing a new frozen graph; there is no silent move or partial profile/packet
  invalidation path.

Several active workstreams may deliberately share one primary checkout. Crewfold
must show that fact wherever the checkout or either workstream is inspected. The
checkout's existing policy remains authoritative:

- `exclusive` serializes writers;
- `claimed` admits concurrent writers only under non-conflicting claims;
- `shared` permits concurrent writers while retaining a visible warning; and
- `read_only` cannot be the primary checkout for source-mutating work.

Crewfold never calls a shared directory isolated. Sequential work reuses it
without penalty; a separate clone or worktree is created and registered only by
an explicit owner workflow.

### Domain-level agents observe the domain; workstream agents inherit its home

A domain membership with no workstream placement is domain-level. Its durable
conversation may inspect every attached checkout through bounded read-only roots
or equivalent no-write Crewfold inspection. It receives no mutation authority
from that breadth.

An agent placed in a workstream receives that workstream, its primary checkout,
its task graph, and relevant domain context as the default scope of its durable
conversation and execution attempts. An agent may still participate in explicit
cross-workstream messages or reference resources, but no folder path determines
its hierarchy or grants.

### Accepted proposals close organization and execution atomically

A work proposal freezes, in addition to its objective and task graph:

- the primary checkout and its revision;
- complete inert definitions for every new durable team member, plus exact
  agent/membership/profile revisions for any existing participant;
- the proposed parent relationship, charter, delegation policy, provider,
  runtime, task class, concurrency, and staffing allocation for every new member;
- the intended workstream placement for each participating agent;
- every exact launch-profile revision;
- each task's agent, class, dependencies, and dependency-output requirements; and
- the same budgets, claim requirements, and scheduling policy already required.

Acceptance atomically creates every proposed new durable agent and launch
profile, places the complete team, creates the Objective, binds its primary
checkout, creates tasks/dependencies/scheduling intents, and records every
effect. Stale checkout, membership, agent, profile, grant, name, or graph state
rejects the entire revision. Submission remains inert: no proposed agent,
membership, profile, objective, task, intent, provider session, or run exists
before acceptance. Rejection has no organizational or execution effect.

Explicit continuing domain-level staff may exist before a work proposal and may
be referenced exactly. Deliverable-specific specialists normally exist only as
proposal-local definitions until acceptance. Neither kind is displayed as a
workstream member until the placement effect commits. Acceptance never infers
placement from a role name, task title, checkout path, or attention ancestry.

### Dependency edges declare the output a successor requires

A dependency is not only an ordering edge. It freezes one delivery requirement:

- `completion`: terminal success and identity/status are sufficient;
- `handoff`: a bounded structured completion handoff is required; or
- `handoff_with_evidence`: the handoff and every exact referenced evidence item
  needed by the successor are required and readable in that successor's scope.

The proposal author must select the requirement. Review-to-remediation,
implementation-to-review, and remediation-to-verification default templates use
`handoff_with_evidence`; the stored value, not the template, governs readiness.

A required predecessor output contains bounded canonical fields: predecessor
task/run identity, completion summary, handoff, changed paths, checks, remaining
risks, unknowns, and authorized evidence references. Raw provider transcripts and
private reasoning are never dependency output.

The scheduler does not start a successor until every required output exists and
is internally consistent. The immutable base context includes those outputs and
records why each was selected. Exact referenced artifact content is available
only through the successor's scoped capability. Missing output produces a
specific unsatisfied-input diagnosis before provider launch, not a running agent
that must discover the omission.

Later messages, accepted knowledge, or changed predecessor outputs may use the
existing bounded delta/rebase mechanism, but a generic task-status delta is never
presented as delivery of a missing handoff.

### One durable agent presents its conversation and attempts as one timeline

The durable identity, owner-facing provider conversation, and task execution
attempts remain distinct authority and process records. They are one logical
coworker in the product.

The selected-agent surface aggregates:

- owner/provider conversation epochs;
- assignments and context packets;
- attached execution lifecycle and readable commands;
- durable messages and tool receipts;
- changed paths and bounded diffs;
- blockers and exact missing inputs;
- checks, evidence, outcomes, and handoffs; and
- epoch rotation/replacement boundaries.

Agent status is derived from the most consequential current state. A durable
conversation process being idle cannot make the agent appear idle while an
attached execution is starting, active, blocked, stopping, lost, or failed.
Process/thread distinctions remain inspectable but secondary.

When an execution completes or blocks, its structured result becomes continuity
input for the durable conversation and appropriate parent roll-up. A replacement
provider epoch begins from bounded canonical continuity, never an entire raw
transcript and never an empty prompt that ignores current work.

### Blockers identify cause, available facts, and a real repair

Every blocker shown to the owner distinguishes at least:

- missing canonical input;
- runtime/environment failure repairable in place;
- stale context requiring a fresh packet/run;
- policy or capacity refusal;
- agent-requested owner judgment; and
- terminal failure requiring acknowledgement or replacement.

The UI shows the blocking entity, expected input/effect, observed state, available
supporting records, and exact next operation. `Resume same runtime` is offered
only when continuing the same process and immutable packet can consume a repair
made in place. Missing predecessor context instead offers deterministic context
repair/rebuild and relaunch. A system-repair operation is not fabricated as an
owner product decision.

## Consequences

- Workstream state becomes the bridge between domain organization and an existing
  local development environment.
- New Codex processes retain clean authority/context boundaries without imposing
  cold filesystem setup.
- Proposal acceptance must evolve as one current contract; no compatibility
  branch keeps partially placed M22 proposals alive.
- Context packets and dependency records gain bounded output closure and artifact
  authorization.
- The scheduler has fewer arbitrary placement choices and better explanations.
- Shared checkouts remain supported but visibly non-isolated.
- Domain-level agents keep broad read-only coordination without becoming writers.
- The web console can remove empty administrative surfaces and derive its three
  primary views—domain, workstream, and agent—from complete canonical state.
- Public OSS release work waits until a real-provider browser workflow proves
  this composition end to end.
