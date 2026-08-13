# ADR-0014: Explicit bounded live context deltas

- Status: accepted
- Date: 2026-08-13

## Context

A context packet is immutable authority intended for at most one run. An explicit
build may leave it unbound until `run.start`; a successful binding is unique. That
property lets an owner explain exactly what an agent received, reproduce
provider-neutral fixtures, and keep later knowledge or collaboration changes from silently
rewriting an active session. It also means a long-running agent cannot learn about
a newly accepted decision, a withdrawn revision, a relevant message, or a newly
authorized participant merely by rereading its original packet.

Rebuilding a packet in place would destroy the audit boundary. Automatically
pushing every event would create unbounded prompt growth, make delivery depend on
provider-specific steering, and confuse “Crewfold made data available” with “the
bound run consumed it.” A live update therefore needs its own immutable,
cursor-based object and an explicit consumption acknowledgement.

The first implementation must remain deterministic without a model, embeddings,
provider transcript, network call, or background scheduler. It also needs a
fail-closed answer when a compact change cannot faithfully preserve the packet's
authority contract.

## Decision

### Packet v4 is the immutable base

New builds use `urn:crewfold:schema:domain:context-packet:v4`. Versions 1 through
3 remain readable historical packets but cannot acquire live deltas. Refreshing
one returns `rebase_required` with reason `unsupported_packet`. Migration must not
invent delivery state for an old run, so this compatibility result creates no
delta-state row or rebase event. Its run-scoped MCP policy never advertises the
delta tools. Rebase means stop or hand off the old run and start a replacement
with a new v4 packet. Crewfold never edits a packet, provider transcript, or
already delivered delta.

In addition to the prior exact role, task, checkout, dependency, inbox,
knowledge, policy, and reporting snapshots, v4 freezes:

- `as_of_event_sequence`, the global journal high-water observed by the packet
  build transaction before the packet-build event;
- the complete direct dependency snapshot, with at most 32 entries; a larger
  direct-upstream set fails packet construction rather than truncating authority;
- `dependents`, a deterministic bounded snapshot of at most 32 reverse dependents
  in the same project sorted by task ID, plus `dependent_task_count` for the full
  canonical total;
- `participant_threads`, at most eight complete participant-thread snapshots
  whose exact agent/task/project roster authorizes the run, selected by newest
  thread update and then thread ID within an 8 KiB collaboration sub-budget; and
- `live_context`, freezing explicit-pull delivery, bound-run acknowledgement,
  one pending delta, a 1,000 relevant-event scan bound, a 16 KiB per-delta bound,
  and a 64 KiB cumulative chain bound.

The roster and reverse-dependent views are informational context, not new
authority. A participant snapshot does not permit a run with another task to read
the thread, and a reverse-dependent snapshot does not permit cross-project task
access. Whole snapshots are selected or excluded; Crewfold never truncates a
roster, message preview, revision, contradiction, or dependency to meet a byte
budget.

Immutability does not make an arbitrarily old prebuilt v4 packet safe to bind.
When `run.start` names one, the binding transaction revalidates its frozen run
authority and requires every embedded knowledge revision still to be accepted,
current, fresh, applicable, and undisputed. A mismatch fails binding and requires
a new packet; the old bytes are not edited. Building and binding in one run-start
transaction has no intervening window. Once binding succeeds, reads preserve the
base as historical authority and later changes are represented only by explicit
refresh withdrawals/deltas or durable rebase. Versions 1 through 3 retain their
separate read-only compatibility behavior and never gain live tools.

### Owner refresh builds; a run fetches and acknowledges

Only the owner-local `context.refresh` operation scans or constructs a delta. It
requires the exact workspace, run, and idempotency key. It deliberately accepts no
caller cursor: the daemon owns the run's durable scan state and derives the base
packet. The operation returns one of:

- `created`: one immutable delta was constructed;
- `pending`: an existing unacknowledged delta was returned unchanged, without a
  scan, delta/state transition, or event;
- `up_to_date`: no eligible change existed and the durable inspected cursor was
  advanced without creating an empty delta; or
- `rebase_required`: safe incremental delivery is no longer possible.

Every successful refresh key records the command's idempotency receipt. Repeating
that key is an exact replay even after later acknowledgement or scanning. A
different key while a delta is pending returns the same delta and records only
that receipt; it cannot manufacture a duplicate or scan past undisclosed work.
Returning already-durable rebase state has the same receipt-only behavior. If a
scan finds no eligible change, advancing the inspected cursor creates no
coordination event and requires no acknowledgement. This prevents empty deltas
from blocking later real changes and prevents Crewfold's own delivery bookkeeping
from feeding the next scan.

The run-scoped MCP tool `crewfold_get_context_delta` has no arguments and only
fetches the sole pending object or current rebase state. It cannot trigger a scan,
choose a workspace, run, task, or cursor, or expand its own context. The
`crewfold_acknowledge_context_delta` tool requires `delta_id`,
`expected_sequence`, and `idempotency_key`. The daemon derives the authenticated
run and accepts only that run's exact pending delta while the run is live. The
owner-local API and CLI intentionally expose no acknowledgement operation: an
owner may make context available, but must not attest that an agent consumed it.

Acknowledgement is immutable and idempotent. A matching retry returns the same
receipt and appends no event; a different request under the same key conflicts.
After acknowledgement, the next explicit owner refresh may scan and create the
next sequence. Daemon restart preserves the pending object, scan cursor, sequence,
cumulative byte count, and acknowledgement state.

### Cursor and projection semantics

Every delta has a monotonically increasing run-local `sequence`, its immutable
base `context_packet_id`, optional `parent_delta_id`, an exclusive
`from_event_sequence`, and an inclusive `through_event_sequence`. The first
`from` cursor is packet v4's `as_of_event_sequence`; later cursors advance only
through daemon-owned refresh state. A delta's content hash covers its canonical
content.

Refresh opens one immediate transaction, reads the global journal high-water as
the cutoff, and selects only potentially relevant event types in the exact
workspace and run authority window `(from, cutoff]`. At most 1,000 relevant
events may be inspected. Unrelated global or workspace activity can be skipped
while still advancing through the cutoff. More than 1,000 relevant candidates
causes durable rebase; Crewfold does not page to an intermediate cursor because
current projections cannot reconstruct canonical state as of that earlier point.

Event payloads identify candidates and provenance, but they are not the delta's
content authority. The transaction reloads current canonical projections,
rechecks exact applicability and participant bindings, coalesces candidates to
their final state, and orders the resulting whole changes deterministically. This
prevents a stale journal payload, later governance transition, or wrong-task
participant event from broadening delivery.

Eligible change kinds are a closed provider-neutral union:

- `message_received`, carrying only the authorized `InboxSummaryItem` preview;
  full message bodies still require the explicit mailbox read operation;
- `knowledge_accepted`, carrying the complete newly eligible accepted/current
  decision or an eligible decision re-offered after dispute closure;
- `knowledge_withdrawn`, either identifying a previously known revision that
  became stale, superseded, expired, or effectively quarantined, or recording a
  durable no-body `disputed` suppression tombstone for a post-base accepted
  applicable decision hidden by an open contradiction, with a replacement when
  one exists;
- `contradiction_opened` and `contradiction_closed`, carrying the complete
  canonical contradiction snapshot and exact affected revisions;
- `dependent_added` and `dependent_updated`, carrying a whole same-project
  reverse-dependent snapshot; and
- `participant_roster_updated`, carrying the complete currently authorized
  participant-thread snapshot after thread creation or participant addition.

Only accepted decisions become newly available knowledge automatically. Findings
remain explicit packet inputs in v1. A finding already known to the run can still
be withdrawn when its canonical eligibility changes. A proposed contradiction is
inert; owner confirmation creates the open/quarantine transition. Dismissal,
resolution, or a participant lifecycle transition can close its effective
dispute. When the final applicable contradiction closes, a decision is re-offered
with cause `contradiction_closed_reoffer` if it was either previously delivered
and then withdrawn solely as disputed, or never delivered but recorded by the
exact suppression tombstone, and it remains accepted, current, fresh, applicable,
and otherwise undisputed. Merely delivering an open contradiction snapshot does
not imply that either participant was delivered or suppressed and cannot cause a
re-offer. Findings and decisions that are stale, superseded, expired, out-of-scope,
or still disputed are not re-offered. Imported canonical state is evaluated by the
same projection rules.

Time-driven freshness expiry is evaluated on explicit refresh even when no new
knowledge event was appended. The resulting withdrawal may therefore have
identical `from_event_sequence` and `through_event_sequence`; its cause records
the deterministic expiry reason. There is no timer-driven push.

### Bounds and fail-closed rebase

A delta must encode as a whole within 16 KiB. The acknowledged plus pending chain
must remain within 64 KiB. It must also preserve the base task, agent, project,
checkout, and direct-dependency contract. Crewfold records `rebase_required`
instead of dropping, truncating, or indefinitely postponing an applicable change
when:

- the base packet predates v4;
- the bound run/task/agent/project/checkout contract no longer matches;
- the direct dependency set or any frozen upstream snapshot changed, which should
  be impossible for an assigned task under current task invariants;
- more than 1,000 relevant events would need inspection;
- one whole delta exceeds 16 KiB or its chain would exceed 64 KiB; or
- a potentially authority-changing event cannot be represented by the closed
  change union.

A newly created reverse dependent is deliverable as `dependent_added`; its later
snapshot change may be `dependent_updated`. Neither alters the run task's
authority. Direct upstream state is part of the frozen execution contract and any
drift rebases rather than becoming a delta. Cross-project dependencies remain
forbidden. Rebase is durable and audited, not a transient error that a caller can
bypass with another cursor or idempotency key.

### Public and audit surfaces

The owner-local API methods are `context.refresh`, `context.delta.list`,
`context.delta.show`, and `context.delta.explain`. The corresponding CLI is
`crewfold context refresh`, `crewfold context delta list`, `crewfold context
delta show`, and `crewfold context delta explain`. Local list/show/explain are
historical inspection; they do not represent delivery.

The immutable event journal records `context_delta.built`,
`context_delta.acknowledged`, and `context_delta.rebase_required`. A no-op scan
cursor is durable projection state but produces no event. MCP policy denial,
scope denial, and ordinary idempotency conflicts retain their existing audit and
stable-error behavior.

## Consequences

- An owner can deliberately refresh a long-running session without rebuilding
  or silently mutating its briefing.
- “Available,” “fetched,” and “consumed” are distinct facts. Only the exact bound
  run can acknowledge consumption.
- Provider adapters need only MCP; no live model, transcript parser, terminal
  prompt injection, or network access is required to prove the protocol.
- Exact cursors, immutable objects, canonical projection reloads, and byte/event
  bounds make restart, replay, and failure behavior independently testable.
- One pending delta introduces deliberate backpressure. The owner or supervisor
  must notice an unacknowledged update before later updates can be built.
- Rebase can interrupt a long-lived run, but does so visibly instead of producing
  an incomplete or misleading incremental briefing.
- Background refresh scheduling, provider steering/resume, owner acknowledgement,
  embeddings or RAG selection, model-written summaries, live claims/artifacts,
  participant removal/broadcast, cross-project dependency authority, portable
  delta export/import, and organization-wide delivery remain future work.

## Rejected alternatives

- Mutate the base packet: this destroys immutable context authority and historical
  explainability.
- Let the run trigger refresh: fetching context must not let an agent advance its
  own authority window or select a different scope.
- Let the owner acknowledge: making data available is not evidence that the bound
  agent consumed it.
- Create an empty delta on every refresh: one-pending backpressure would make a
  no-op object block the next meaningful update.
- Append every event payload: event data is candidate provenance, not a canonical
  or safely scoped delivery snapshot.
- Truncate or page an oversized change set: partial canonical state can invert the
  meaning of knowledge, contradiction, participant, or reverse-dependent
  transitions.
- Use FTS or embeddings to choose updates: live eligibility is an exact authority
  projection, not a relevance search.
