# Milestone review — structured meetings and consolidation

## Identity

- Milestone: `M13 — Structured meetings and consolidation`
- Review status: `passed`
- Implementation commit: `432e6e02a85c4793ec26f08bfcfc7783a587d04d`
- Reviewer: `automated deterministic acceptance and repository gate`
- Date: `2026-08-12`

## Demonstrable outcome

- User-visible capability: freeze an open overlap into a durable two- or
  three-participant decision procedure, preserve independent positions across
  daemon restart, inspect a typed facilitator proposal, require configured
  authority, and atomically apply explainable task/claim actions.
- Acceptance scenario: `test/scenarios/structured-meetings/run.sh`.
- Focused command: `test/scenarios/structured-meetings/run.sh`.
- Full command: `./scripts/check.sh`.
- Expected result: the focused scenario prints
  `Structured meeting acceptance: PASS`; all earlier offline, race, and black-box
  gates pass without provider credentials, inference, or network access.
- Public surface: `meeting create`, `meeting run`, `meeting inspect`,
  `meeting accept`, and `meeting takeover`, with versioned local API schemas.

## Procedure and authority contract

Creation freezes the overlap, both claims and tasks, all selected agent
definitions, the event cursor, and a content hash. Two or three participant
agents are distinct from the facilitator; a named reviewer is independent from
both. A participant has at most one position contribution in the round.
Identical retry reuses it, replacement is rejected, and an outsider contribution
is rejected.

The facilitator can propose only typed actions over frozen meeting entities:

| Action | Durable effect |
| --- | --- |
| `sequence` | Add a dependency, release the downstream overlapping claim, resolve its hold |
| `split` | Create a new ready task inheriting project/objective/priority/budget |
| `reassign` | Replace a ready/assigned task lease when no live run exists |
| `designate_role` | Record an explicit implementer or reviewer task duty and provenance |
| `cancel` | Cancel a nonterminal task, release its assignment and overlapping claim |

Unknown payload fields are rejected. Every action set first revalidates frozen
overlap, claim, and task revisions, then commits all projections/events or none.
Owner policy never applies a proposal before explicit `meeting.accept`.
Named-reviewer policy requires its recorded review; bounded-manager policy can
apply only allowlisted action types. Anything else waits for the owner.
Implementer and reviewer duties are meeting-scoped workflow metadata, not fixed
`AgentDefinition.Role` values and not sources of authority.

## Test evidence

| Suite | Command | Result | Evidence |
| --- | --- | --- | --- |
| Store/state machine | `./scripts/go.sh test ./internal/store` | passed | frozen input, two/three participants, independent reviewer, every action type, authority gates, stale input, timeout, retry, outsider/unknown-field rejection |
| Restart and rollback | store tests plus focused scenario | passed | durable position checkpoint, reopen/resume without recollection, missing participant recovery, injected transaction rollback |
| Daemon/CLI/protocol | `./scripts/go.sh test ./internal/daemon ./internal/cli ./protocol` | passed | five API methods, repeatable participant flags, strict fixtures, published JSON Schema IDs |
| Typed persistence | `./scripts/sqlc.sh vet` and generated consistency gate | passed | pinned generator, typed params/results/nullability, source and generated-output hashes |
| Black-box acceptance | `test/scenarios/structured-meetings/run.sh` | passed | real binary/socket/database, daemon restart, owner acceptance, named-reviewer workflow, stalled human takeover |
| Full offline/race gate | `./scripts/check.sh` | passed | vet, all tests, race tests, and all fourteen capability scenarios |

## Failure proof

- Missing participant: one position commits, the other participant becomes
  `missing`, and the meeting becomes `stalled` without losing the contribution.
- Facilitator restart: both positions commit at `facilitator_pending`; after a
  daemon stop/start, a proposal-only fixture advances the same frozen meeting and
  exactly two positions remain.
- Unapproved owner policy: the proposal is visible at `awaiting_approval`, while
  task revision/dependencies and both claims remain unchanged. Acceptance then
  adds the dependency and resolves the overlap.
- Stale input: an external task revision between proposal and acceptance returns
  `meeting_input_stale`; no action or partial mutation persists.
- Injected persistence failure: the hook fires after projections and rolls back
  contributions, proposal, actions, meeting revision, events, and idempotency.
- Invalid authority/input: out-of-policy manager actions wait for approval;
  outsider positions and unknown action fields fail before any meeting state is
  changed.

## Persistence and recovery

- Schema v9 adds meetings, participants, contributions, proposals, actions, and
  provenance-linked task roles.
- Frozen JSON is canonical state, not a terminal transcript. Its hash and source
  revisions make later acceptance auditable and stale-safe.
- Contribution uniqueness and command idempotency survive process restart.
- Human takeover is an explicit owner mutation that can resolve a stalled
  procedure from a typed proposal; it is never triggered silently.
- Sequence/cancel reuse claim-resolution transactions, so task, claim, overlap,
  hold, action, proposal, meeting, event, and idempotency changes are atomic.

## Typed database boundary

ADR-0007 adopts `sqlc` rather than adding a Node/TypeScript ORM runtime. Ordered
embedded SQL migrations remain authoritative, named queries generate checked-in
Go accessors, and domain services retain transaction and policy ownership. The
generator is pinned at 1.31.1. Normal builds remain offline; the gate verifies
both source freshness and generated-output integrity without requiring `sqlc`.

Structured-meeting storage uses typed generated access. One recursive dependency
cycle query remains explicit because the SQLite analyzer cannot type that CTE;
the same bounded query is already covered by dependency and meeting tests.

## Security and autonomy

- Meeting fixtures and proposals are bounded by the existing 64 KiB local API
  request limit; summaries, evidence counts/sizes, participant counts, actions,
  deadlines, and leases have additional domain bounds.
- No proposal can reference an arbitrary external task or agent.
- No live run can be reassigned or cancelled by a meeting action.
- No owner-gated or out-of-policy proposal mutates work.
- The accepted action set changes Crewfold coordination state only. It does not
  edit source, run Git writes, push, merge, deploy, contact a person, or launch a
  paid model.
- Recorded Codex and Claude regression scenarios made no network/model call.

## Compatibility

- Storage advances additively from schema v8 to v9. Existing rows are unchanged;
  no historical meetings or roles are fabricated.
- Local API v1 gains additive methods and result schemas. Existing envelopes and
  all M0–M12 scenarios pass unchanged.
- An older binary safely refuses schema v9 as newer. Downgrade requires restoring
  a schema-v8 backup rather than dropping meeting evidence.
- Runtime/provider interfaces are unchanged. Meetings currently use deterministic
  fixtures, so live Codex, Claude, and Herdr behavior is unaffected.

## Known limitations and deferrals

- The procedure is invoked explicitly from one detected overlap; automatic
  meeting scheduling belongs to the manager/supervisor milestone.
- Deterministic fixtures stand in for participant/facilitator provider runs. Live
  MCP participation and provider resume are later integrations, while the durable
  state machine is already provider-neutral.
- A sequenced downstream task releases its current overlapping claim and must
  acquire a fresh claim when its dependency later becomes ready.
- A split task is created ready but receives no automatic claim or assignment.
- Task-role records are visible through meeting inspection; a task-centric role
  query can be added when scheduling consumes secondary roles.
- Meetings preserve structured positions, evidence references, and decisions—not
  transcripts. Canonical cross-task decisions and curated retrieval begin in M14.
- Personal SQLite authority, one local owner, and bounded local concurrency remain
  intentional. Multi-user federation is outside the personal milestone path.

## Repository hygiene

- Working tree clean after implementation commit and acceptance: yes before this
  review/roadmap record was added.
- No leaked processes, sockets, or temporary scenario directories: yes.
- No paid/network call in default tests: yes.
- Public upstream created: no.
- Files or variables named after the milestone identifier: no; implementation
  names describe meetings, actions, queries, and roles.
- Documentation matches behavior: yes.

## Decision

- Exit gate satisfied: `yes` — accepted two-/three-agent resolutions create real,
  explainable task/claim state without reconstructing terminal transcripts.
- Waivers and accepting authority: none.
- Next milestone entry criteria met: `yes`; canonical knowledge may begin.
- Next question: can an accepted meeting decision become durable, provenance-linked
  knowledge that a replacement agent receives without copying a transcript?
