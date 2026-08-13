# ADR-0012: Owner-confirmed exact knowledge contradictions

- Status: accepted
- Date: 2026-08-13

## Context

Canonical revisions are immutable and retrieval is derived. Two accepted/current
revisions can nevertheless make incompatible claims. Silently ranking one above
the other would turn retrieval into governance; adding `disputed` to the currency
enum would conflate factual conflict with staleness or supersession; and rewriting
old context packets would destroy the exact authority a run received.

Agents are often best placed to notice a conflict, but a report is not sufficient
authority to quarantine accepted knowledge. The owner needs a bounded,
explainable decision surface whose effects are consistent in search and explicit
context assembly, and whose state survives retries and restart.

## Decision

### Separate exact-pair record

A contradiction is a durable `kcon_...` record over two lexically ordered exact
`krev_...` IDs. The revisions must be different knowledge items in the same
workspace and project, both accepted/current, with intersecting applicability:
two project-wide claims, one project-wide plus one task claim, or the same exact
task. Two different task scopes do not intersect.

The globally canonical pair is unique for all time. Reversing the request order
does not create a new identity. A dismissed false positive or automatically
resolved historical conflict cannot be re-reported as another record because the
two immutable bodies have not changed; a materially changed claim needs a new
knowledge revision.

Contradiction state is `proposed`, `open`, `dismissed`, or `resolved`, with its own
optimistic state revision. Knowledge review and currency remain unchanged. The
knowledge-revision v1 schema and context-packet v3 schema are not extended.

### Reporting and governance authority

The local owner may report a valid pair. A live authenticated run may also report
when both revisions belong to its project and each is either project-wide or
scoped to its exact task. Run identity, workspace, project, and task are derived
inside the report transaction; tool arguments cannot select them. `starting`,
`active`, and `blocked` are the only live reporter states. A committed report may
still replay exactly after the run completes because idempotency is checked before
fresh-state eligibility.

A report starts `proposed` and has no retrieval effect. Only the local owner may
confirm it `open`. Confirmation revalidates both exact revisions and their scope
intersection in the same transaction. The owner may dismiss a proposed or open
record. Every owner decision carries an optimistic expected state revision and a
durable allowed authority check.

The store also defines non-owner confirmation/dismissal denial as a durable
authority event and check, evaluated before optimistic revision conflict and
replayed idempotently. The run MCP capability does not advertise governance.
`crewfold_confirm_contradiction` is only a reserved known name so an attempted
probe becomes `run.tool_denied` and never reaches contradiction governance.

### Conservative whole-revision quarantine

Only an `open` contradiction creates effective dispute. Each exact participant is
then quarantined everywhere that revision would otherwise apply. Applicability
intersection gates whether a pair can be reported and confirmed; it does not
limit the quarantine to the overlap. Thus a project-wide participant contradicted
by a task-scoped participant is excluded from project-only search and from every
other task until the open record closes. This intentionally chooses safety over a
scope-specific reinterpretation of an immutable project-wide claim.

`knowledge.dispute` derives the total number of incident open contradictions and
the first at most 200 IDs in lexical order. It never writes a second currency
field. The count discloses truncation.

### Retrieval and context behavior

Search excludes open-contradiction participants relationally inside its canonical
eligibility query, before ranking and `LIMIT`. Proposed, dismissed, and resolved
records have no search effect.

An explicit context build first applies its existing scope, review, currency, and
freshness rules. An ID already excluded for one of those reasons remains an
ordinary exclusion and does not reveal unrelated contradiction state. If an
otherwise eligible explicitly requested revision participates in any open
contradiction, the whole build transaction fails with stable
`knowledge_conflict`; budget evaluation happens later. The error includes the
first 16 sorted unique authorized contradiction IDs and the exact `(+N more)`
suffix when needed. No packet, event, or idempotency result is committed on
failure, so the same request can succeed after resolution.

Previously built packet-v3 bytes are immutable. Opening, dismissing, resolving,
or discovering a contradiction never rewrites an existing packet or briefing.

### Terminal resolution

Owner dismissal closes a proposed or open record. Marking either participant
stale, or accepting a successor that supersedes it, atomically resolves every
incident open contradiction. Each resolution records which participant became
stale/superseded, the exact governing knowledge event and authority actor, and a
`contradiction.resolved` event. A revision remains disputed while any other open
incident record remains.

### Bounded explanation and persistence

Show and list return the contradiction plus both complete exact revision
snapshots. Authority history discloses `authority_check_count` and only the newest
at most 200 checks, ordered by event sequence then ID descending. List requires a
project, defaults to the newest 50 active (`proposed|open`) records, accepts at
most 200, and has no cursor in v1. An explicit status can select terminal records.

SQLite triggers independently enforce canonical ordering, absolute pair
uniqueness, valid UTF-8/byte bounds, participant eligibility at insert and
confirmation, live run reporting, immutable identity, legal lifecycle revisions,
and exact event/authority linkage. Report, governance, automatic resolution,
projection, event, and idempotency writes share one transaction. Named SQL is
generated through the pinned `sqlc` boundary.

## Consequences

- An agent can raise a conflict without gaining the power to quarantine accepted
  knowledge.
- Search and explicit context assembly fail closed from the same relational open
  state while historical packets remain explainable and immutable.
- A broad claim may temporarily disappear from unrelated tasks. Owners resolve
  that conservative blast radius by dismissing the report or governing a new
  revision, not by hidden ranking.
- Exact pair history is compact and cannot accumulate duplicate reverse-order or
  post-dismissal records.
- Context refresh/deltas, semantic detection, and automated reconciliation remain
  separate work. The portable snapshot defined later by
  [ADR-0013](0013-portable-project-knowledge-snapshots.md) preserves contradiction
  IDs, exact participant links, descriptive lifecycle state, and effective open
  dispute without flattening dispute into knowledge currency. Its deliberately
  narrower trust boundary does not export or replay this origin event/authority
  ledger; the importing local owner attests the restored final state.

## Rejected alternatives

- Add `disputed` to knowledge currency: conflates independent lifecycle axes and
  would require mutating the frozen revision contract.
- Let a reporting run open the record: allows an agent assertion to revoke
  owner-accepted authority.
- Quarantine only within the pair's scope intersection: leaves a contradicted
  project-wide claim authoritative elsewhere without a new scoped revision.
- Resolve conflicts by search score or a model: makes discovery or prose an
  implicit governance mechanism.
- Rewrite old packets after confirmation: destroys replayability and the record
  of what an earlier run was actually told.
