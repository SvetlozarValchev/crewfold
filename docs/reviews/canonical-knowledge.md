# Milestone review — canonical knowledge and explicit context

## Identity

- Milestone: `M14 — Canonical knowledge and explicit context packets`
- Review status: `passed`
- Implementation commit: `e37fdcf32b2e5f69766405d6585ff24277a1ab3c`
- Reviewer: `automated deterministic acceptance, adversarial invariant review, and repository gate`
- Date: `2026-08-13`

## Demonstrable outcome

- User-visible capability: preserve a concise decision or finding as an immutable,
  provenance-linked revision; require explicit owner governance; select an exact
  accepted/current revision into a bounded immutable context packet; and let a
  replacement agent continue across providers without reading the earlier
  provider's transcript.
- Acceptance scenario: `test/scenarios/canonical-knowledge/run.sh`.
- Focused command: `test/scenarios/canonical-knowledge/run.sh`.
- Full command: `./scripts/check.sh`.
- Expected result: the focused scenario prints
  `Canonical knowledge and provider-switch acceptance: PASS`; all earlier offline,
  race, and black-box gates pass without credentials, inference, or network access.
- Public surface: `knowledge propose`, `show`, `list`, `accept`, `reject`, and
  `mark-stale`; repeatable `context build --include`; the current context packet; and the
  run-scoped `crewfold_propose_knowledge` tool.

## Knowledge, authority, and provenance contract

A stable `know_...` item owns numbered `krev_...` content revisions. M14 supports
only `decision` and `finding`. Title, body, quality metadata, freshness policy,
supersession link, and ordered structured sources are immutable after proposal.
Governance advances a separate state revision.

Review and currency remain independent:

| Operation | Review/currency result | Authority |
| --- | --- | --- |
| Propose new item or successor | `proposed/pending` | Local owner or authenticated run |
| Accept | `accepted/current` | Local owner only |
| Reject | `rejected/pending` | Local owner only |
| Mark stale | `accepted/stale` | Local owner only |
| Accept successor | predecessor `accepted/superseded`; successor `accepted/current` | Local owner only, one transaction |

Every revision has exactly one primary source and at most 15 supporting sources.
Sources are a task, concluded meeting, or accepted proposal from a concluded
meeting, frozen at their source revision. All sources must share a workspace and
project. The primary source derives project scope; optional task scope narrows
applicability independently from provenance.

Public governance payloads never select their actor. The owner-only local socket
injects `local-owner`. An authenticated run can only propose, with its actor,
workspace, project, task, and primary source derived from its capability. No agent
accept/reject/stale tool is advertised. Reserved governance probes stop at the
capability layer as durable `run.tool_denied` events. Defense-in-depth tests also
prove that a non-owner operation reaching the store commits a `kauth_...` denial
and action-specific denial event without changing the proposal.

## Exact context contract

`context build --include krev_...` accepts at most 16 unique exact revision IDs in
caller order. It performs no project scan, full-text search, semantic retrieval,
or automatic successor substitution. Selection happens atomically with packet
creation.

A complete revision snapshot is included only when it is accepted, current,
fresh, in the task's workspace/project, and compatible with optional task scope.
Known ineligible candidates are explained individually as `proposed`, `rejected`,
`stale`, `superseded`, `out_of_scope`, or `over_budget`. A superseded pin may name
its current replacement, but only an explicitly requested replacement is included.
Unknown IDs fail the build.

The current packet records the ordered request, complete selected snapshots, selections,
exclusions, and byte accounting. The total limit is 32 KiB and the accepted-
knowledge sub-budget is 12 KiB. Revisions are included whole or excluded; no body
is truncated. Eligibility freezes at build time. Later acceptance, expiry,
staleness, or supersession neither rewrites the packet nor invalidates its run
binding; a new build re-evaluates current state.

## Test evidence

| Suite | Command | Result | Evidence |
| --- | --- | --- | --- |
| Store/state machine | `./scripts/go.sh test ./internal/store` | passed | proposal/accept/reject/stale/supersede lifecycle, exact history, task/meeting provenance, scope derivation, state conflicts, idempotency, owner and denial authority |
| Integrity and rollback | store tests | passed | sealed sources, strict IDs, legal-only governance transitions, immutable packets, predecessor/successor rollback after injected projection/event failures |
| Context contract | store/domain/protocol tests | passed | current-packet exact selection, all exclusion reasons, stable hashes/budgets/order, restart-stable show/explain |
| Daemon/CLI/MCP | `./scripts/go.sh test ./internal/daemon ./internal/cli` | passed | strict local methods, repeated includes, agent proposal, absent governance tool, policy-denial audit, project-scoped listing |
| Typed persistence | generated database consistency gate | passed | current baseline, checked-in `sqlc` accessors, source and generated-output hashes |
| Black-box acceptance | `test/scenarios/canonical-knowledge/run.sh` | passed | real binary/socket/SQLite, recorded Codex handoff, daemon restart, explicit packet binding, recorded Claude replacement, supersession, sentinel exclusion |
| Full offline/race gate | `./scripts/check.sh` | passed | vet, all tests, race tests, and all fifteen capability scenarios |

## Failure proof

- Unauthorized governance: an internal `agent_run` acceptance attempt leaves the
  revision `proposed/pending`, commits one denied authority check and denial event,
  and replays idempotently. A reserved MCP acceptance probe separately commits
  `run.tool_denied` and creates no knowledge authority record.
- Interrupted successor acceptance: injected failures after projection and after
  events roll back predecessor supersession, successor acceptance, both authority
  records/events, and idempotency. The same command then succeeds on retry.
- Ineligible context: proposed, stale, task-scoped cross-task, and oversized
  accepted revisions remain absent with stable per-ID reasons; accepted applicable
  content remains whole.
- Supersession: the old exact ID remains inspectable but is excluded from a fresh
  packet with replacement metadata. The successor appears only when its own ID is
  requested. A packet created before supersession remains byte-for-byte unchanged.
- Restart: daemon stop/start preserves knowledge show, packet show, and context
  explanation output byte-for-byte.
- Transcript boundary: a unique sentinel emitted only to recorded Codex terminal
  logs is present in run logs but absent from context responses, the Claude run,
  and SQLite/WAL after clean shutdown.
- Database tampering regressions: direct content, source, governance-metadata,
  illegal-state, packet-update/delete, and malformed-ID mutations are rejected by
  SQLite constraints or triggers.

## Persistence and recovery

- The current schema includes knowledge items, immutable content revisions, ordered sources,
  and append-only authority checks. It also enforces the previously documented
  immutability of stored context packets.
- The `knowledge.proposed` event seals a revision's source set. Accepted/rejected/
  stale/superseded transitions are the only legal database state changes, each
  advancing the state revision through store-owned transactional commands.
- Successor acceptance updates both revisions, authority history, events, and
  idempotency atomically. Rejection of a successor leaves its predecessor current.
- A same-key context replay returns the frozen original packet even after knowledge
  governance changes. A new key performs a new eligibility evaluation.
- Context packet JSON contains the exact accepted snapshot needed after restart;
  no live knowledge lookup or provider transcript is required to brief a run.

## Typed database boundary

Canonical storage uses named `sqlc` queries and checked-in generated Go accessors.
The store retains transaction, authority, validation, and event ownership. The
generated-source and output hashes are part of the normal offline gate, so builds
do not require a locally installed generator while stale generated code fails CI.

The baseline uses SQLite checks, unique indexes, and triggers for item/revision/
source/authority and context-packet invariants. Runtime reads never assemble
canonical state from ad-hoc terminal logs or a second persistence system.

## Security and autonomy

- The local API remains owner-only and exposes no actor/approver field.
- Run capabilities expose proposal only; task/project/source identity cannot be
  forged in MCP arguments, and a frozen packet does not gain an ungranted tool.
- Knowledge title/body, notes, sources, list sizes, include counts, and packet
  bytes are bounded. IDs and enums are strict in both JSON Schema and SQLite.
- Exact pins make briefing input reproducible and prevent retrieval or a new
  successor from silently changing an agent's authority.
- Full provider transcripts are neither ingested nor queried. Recorded provider
  scenarios make zero model/network calls.
- No knowledge operation edits source, performs Git writes, pushes, merges,
  deploys, contacts people, or grants task-completion authority.

## Current contract

- Fresh storage contains no fabricated knowledge. The current context packet uses
  explicit empty knowledge arrays when no revisions are requested.
- Local API v1 exposes knowledge methods and current context results under the
  same exact envelope used by provider adapters and all M0–M13 scenarios.

## Known limitations and deferrals

- Exact revisions must be selected explicitly. Deterministic search, FTS, ranking,
  curator queues, contradiction detection, and context deltas begin in M15.
- M14 supports decisions and findings only. Briefs, constraints, glossary entries,
  risks, runbooks, summaries, labels, component scope, sensitivity, and export are
  deferred.
- Expiring knowledge is evaluated when a new packet is built; existing packet
  snapshots remain intentionally frozen.
- Agents may propose but never self-accept. All governance uses one local owner;
  multi-user authority and organization-wide federation remain outside the
  personal milestone path.
- Provenance references structured Crewfold entities, not arbitrary transcript
  excerpts or external documents. Rich artifact/evidence policy remains future
  work.

## Repository hygiene

- Working tree clean after implementation commit and acceptance: yes before this
  review/roadmap record was added.
- No leaked processes, sockets, or temporary scenario directories: yes.
- No paid/network call in default tests: yes.
- Public upstream created: no.
- Files or variables named after the milestone identifier: no; implementation
  names describe knowledge, revisions, authority, context, and provenance.
- Documentation matches behavior: yes.

## Decision

- Exit gate satisfied: `yes` — a replacement agent completed from a durable
  cross-provider handoff plus one exact accepted knowledge revision, while
  unrelated terminal output remained outside canonical authority.
- Waivers and accepting authority: none.
- Next milestone entry criteria met: `yes`; curator/retrieval work may begin only
  against canonical records and may not grant acceptance authority.
- Next question: can Crewfold find, reconcile, and refresh relevant knowledge at
  larger volume without making retrieval the source of truth?
