# Storage contract

Status: implemented as one current greenfield schema baseline.

## Location and ownership

The foreground daemon opens `<data-dir>/crewfold.db` only after it holds the
exclusive `<data-dir>/daemon.lock`. A newly created data directory uses mode
`0700`; the database and lock use `0600`. Crewfold refuses a database path that is
a symbolic link or a non-regular file.

Crewfold writes the ASCII application ID `CRFD` into SQLite's file header. An
unidentified database with a nonzero schema marker or user tables is preserved
and refused rather than adopted as Crewfold storage.

The database is opened through the CGO-free `github.com/ncruces/go-sqlite3`
`database/sql` driver. The driver and its transitive build dependencies are
vendored so `GOPROXY=off` builds and tests do not depend on a warm module cache.

## Connection policy

Crewfold currently uses one open/idle database connection. This serializes a very
small mutation volume, makes per-connection SQLite settings unambiguous, and keeps
crash behavior easy to verify. Later load tests may justify a bounded pool without
changing command or event semantics.

Every connection requires:

| Setting | Value | Purpose |
| --- | --- | --- |
| `journal_mode` | `WAL` | Durable crash recovery with concurrent readers |
| `foreign_keys` | `ON` | Enforce projection/event relationships |
| `busy_timeout` | 5000 ms | Bound transient lock contention |
| `synchronous` | `FULL` | Favor local durability over mutation throughput |
| transaction lock | `IMMEDIATE` | Acquire the write reservation before invariant checks |

Startup fails before the API socket is bound if baseline initialization or canonical database
health checks fail. Crewfold opens a short-lived base SQLite connection without
registering the FTS5 module and runs one global `PRAGMA quick_check(1)`. This checks
database-wide page allocation, the freelist, and every ordinary B-tree—including
the FTS shadow tables—without invoking the disposable virtual table's semantic
`xIntegrity` hook. Any failure remains `storage_failed`; there is no error-string
classification or table-filter fallback. `database.status` reports this global
physical/canonical result alongside schema version, journal mode, and the
foreign-key setting. Retrieval projection semantics are checked and reported
separately.

## Embedded current baseline

The current SQL baseline under `internal/store/migrations/` is embedded into the
binary. A fresh data directory is initialized transactionally, and SQLite
`user_version` plus `schema_migrations` identify the one schema understood by the
binary. Tests construct that baseline from empty storage and exercise canonical
integrity against representative current records; no historical upgrade path is
part of the greenfield contract.

## Workspaces, events, and idempotency

`workspaces` is the current-state projection. It stores an opaque stable ID,
unique human name, revision, timestamps, and creating/updating actor IDs.

`events` is the immutable local journal. Its integer primary key is the strictly
increasing local cursor. Each row also has an opaque event ID, event type/schema,
two timestamps, actor, workspace, entity/revision, correlation/causation IDs, and
validated JSON data.

`idempotency_keys` stores the command name, canonical request hash, and exact
successful domain result. Keys are globally unique within this local database in
M2. Retention/compaction is deferred until command volume makes it necessary.

`schema_migrations` records the embedded baseline applied to the database.

## Projects, repositories, and checkouts

`projects` scopes a named coordinated body of work to a workspace.

`repositories` stores an observed Git-history identity: object format, sorted root
commits, and their derived fingerprint. It deliberately stores no filesystem path
as repository identity.

`project_repositories` permits one project to span multiple histories and avoids
duplicating a workspace repository identity.

`checkouts` stores concrete normalized paths, write modes, availability,
standalone/linked-worktree kind, branch, HEAD, dirty state, Git metadata paths,
observation diagnostics, and durable revisions. The path is unique on the local
node. A missing path updates availability; it does not delete the row.

Project and checkout registration update all projections, append their events,
and record idempotency responses in one transaction. Git probing happens before
that transaction and uses only bounded read commands.

## Agents, objectives, and tasks

`agents` stores provider-neutral durable definitions: scoped name, role, provider,
runtime preference, enabled state, concurrency configuration, revision, and audit
metadata. It contains no process or provider session handle.

`objectives` scopes a title, lifecycle status, and token/cost/time budget to one
project. `tasks` stores project/objective scope, title/description, coordination
state, blocked reason, priority, budget, revision, and audit metadata.

`task_dependencies` is a same-project directed graph. The store checks cycles with
a recursive CTE before inserting an edge. `task_assignments` retains assignment
history and lease timestamps. A partial unique index permits only one row in
`active` state for a task; expiry and cancellation change the row state rather
than deleting it.

Agent/objective/task mutations append events and store idempotent results in the
same immediate transaction as projection changes. Expected revisions are checked
inside that write transaction, so concurrent stale writers cannot both succeed.
Readiness is a deterministic query over task state and incomplete dependencies;
it is not stored as an independently drifting boolean.

## Runs and deterministic execution

The task state constraint includes `review`, `changes_requested`, and `failed`
for evidence-driven run outcomes.

`runs` stores committed execution intent, task/agent/checkout placement, opaque
runtime/provider names and handles, the validated fake scenario, normalized
cursor, result/failure state, revisions, and explainable placement reasons.
`run_jobs` is the durable pending/leased/complete worker queue. `run_timeline`
stores bounded normalized facts rather than raw provider transcripts.
`run_handoffs` stores exactly one accepted completion handoff per run.

A partial index permits only one live requested/starting/active/blocked run for a
task. Additional indexes bound workspace, agent, checkout, queue, and timeline
queries. Run intent, queue insertion, first timeline fact, event append, and
idempotency response commit atomically. Worker transitions update run/task state,
timeline, handoff, assignment release, and events in transactions separate from
adapter effects.

## Direct-runtime supervision

Run state includes `stopping`, `stopped`, and `lost`, plus bounded stop-grace and
forced-stop facts.

The live-run uniqueness and checkout-capacity indexes include `stopping` and
`lost`. A lost process may still be writing, so uncertainty cannot silently free
its assignment or checkout. Direct supervisor files live under owner-only daemon
state rather than SQLite; the database stores only the opaque runtime handle and
coordination meaning. Each supervisor state file is atomically replaced and
contains process identity, exit/timeout/stop result, output byte counts, and an
explicit unknown state when identity cannot be verified.

## Context packets and run-scoped MCP

`context_packets` stores an immutable bounded JSON packet, semantic SHA-256 hash,
byte size, task/agent/checkout scope, and creation provenance. The packet includes
its own exact entity revisions and selection/exclusion explanation. Each
`run_context_bindings` row binds one packet to one run; both sides are unique.

`run_capabilities` stores only expiry—not the credential. A private node key under
daemon state derives per-run HMAC tokens, and private token files give direct
children capability access without putting secrets in SQLite or launch specs.

`run_reports` durably sequences idempotent progress, blocked, and completion
proposals in submission order. Applying a report and advancing run/task state is
one transaction. `run_artifacts` stores at most 32 KiB of UTF-8 text with a content
hash and run-local idempotency key. `run_tool_calls` records allowed, denied, and
errored MCP operations without request bodies or credentials.

Capability expiry and terminal run state are both checked on each MCP request.

## Durable messaging

`message_threads` stores workspace/project/task scope, a bounded subject, open or
closed state, revision, and actor provenance. `messages` stores immutable sender,
kind, bounded body, artifact links, reply link, and creation time.
`message_recipients` stores mutable queued/delivered/read/acknowledged state and
timestamps separately from the message. The current command creates exactly one
recipient row; the table keeps recipient state explicit for later evolution.

`message_wake_jobs` is a separate durable queue for best-effort delivery to an
already-live recipient run. Sending to an offline agent commits mail without a
wake job. Sending to a live agent commits a pending wake intent atomically with
the message; daemon startup reclaims pending or expired leased jobs. Wake success
may advance a still-queued delivery to delivered. Wake failure stores a bounded
diagnostic and leaves delivery queued so later inbox polling remains authoritative.

Message sends are idempotent within their sender identity, and read/acknowledge
mutations are idempotent within the authenticated run. Artifact references are
validated against the sender run. A run inbox is restricted to its agent and
project; owner inspection is wider but does not mutate delivery state. The current
context packet carries a bounded inbox snapshot and advertises mailbox tools only
when they are present in its frozen allowlist.

## Work claims, overlaps, and drift

`checkouts.dirty_paths_json` stores the sorted repository-relative paths from the
most recent bounded Git observation in addition to the coarse dirty boolean.

`work_claims` stores one task's leased path/component/operation declaration,
optional concrete checkout, mode, conflict policy, immutable baseline dirty paths,
lifecycle status, revision, and actor provenance. A partial unique index prevents
duplicate active declarations for the same task/scope/checkout while retaining
expired and released history.

`work_overlaps` stores canonical claim/task pairs, a concrete intersection witness,
deterministic severity and effective policy, scheduling/resolution flags, an
explanation, lifecycle state, and resolution reason. `task_coordination_holds`
maps pause-policy overlaps to the affected tasks; `run.start` refuses a held task
without mutating existing runs.

`claim_drifts` stores per-task/checkout/path observations outside the task's active
claim union. It retains first/last/resolved times, HEAD, restart-gap evidence, and
revision. `checkout_claim_scans` stores the last watcher identity, HEAD, dirty-path
set, and observation time so a new daemon can distinguish a continuous scan from
an observation gap. Repository identity is not used as checkout identity.

Claim creation, overlap projection, policy holds, journal events, and idempotent
response commit atomically. Denied claims commit none of those records. Release
or expiry resolves related overlaps and removes their scheduling holds in the
same transaction. Git scans are external read-only observations followed by a
separate atomic checkout/drift update.

## Meetings and canonical knowledge

The current baseline stores frozen structured meetings, participant checkpoints,
independent contributions, typed proposals/actions, and authority/application
records. Meeting resolution commits its complete authorized action set atomically.

It also stores stable knowledge items, immutable-content revisions,
ordered frozen provenance, and append-only governance-authority records. Database
constraints permit only proposed acceptance/rejection, current staleness, and
atomic predecessor supersession. It also enforces update/delete rejection for
stored immutable context packets. The current packet embeds exact accepted revision
snapshots; the packet remains canonical even if later governance changes.

## Retrieval projection

`knowledge_search` is a disposable SQLite FTS5 projection over canonical revision
IDs, workspace IDs, titles, and bodies. `knowledge_search_metadata` publishes one
completed generation with build time, source count, deterministic digest, and
`source_event_sequence`. That sequence is the transactionally observed high-water
mark of the node-wide event journal—not a retrieval-freshness check or a
workspace-scoped knowledge-event cursor. Neither table is a knowledge authority.

Search validates the projection against canonical revision count/digest and FTS
integrity before returning candidates. Missing, corrupt, inconsistent, or stale
state is a degraded retrieval diagnosis, not a database-startup failure and not an
empty successful query. An explicit rebuild reconstructs and validates the FTS
table transactionally, then publishes the next generation. Exact knowledge,
context, and event reads never depend on the projection.

The module-free startup connection deliberately cannot invoke FTS semantic
integrity, so a malformed segment payload leaves retrieval degraded and keeps
`knowledge index rebuild` reachable. Its global check still observes canonical
corruption, freelist/page-allocation damage, and structural damage to ordinary FTS
shadow B-trees. Simultaneous FTS semantic and canonical corruption therefore still
blocks startup.

## Participant-bound collaboration

`message_threads.kind` distinguishes `direct` threads from
`participant_bound` collaboration, and `participant_revision` provides optimistic
roster concurrency. No cross-project authority is inferred from a direct thread.

`thread_participants` stores two through eight immutable owner-created bindings.
Each freezes one enabled agent, its exact active unexpired assignment and task,
the task's project, their display names and observed revisions, ordinal,
invitation time, and inviter.
Agents and tasks are independently unique within one roster so an agent-only MCP
recipient always resolves to exactly one binding.
An initial participant thread spans at least two projects. Later invitations add
one binding and advance the roster revision atomically. Participants cannot be
rewritten or deleted.

`message_recipients.recipient_participant_id` freezes which exact binding
authorized a participant-thread delivery. Inserts enforce that sender runs match
the bound agent/project/task and recipients belong to the same thread. Participant
messages reject artifacts. Inbox, read, acknowledgement, context summary, wake
selection, and wake completion all re-check the exact run binding; direct rows
retain the project-scope rule. Message bodies stay immutable and retain
the authenticated sender run's origin project/task.
Owner participant mail cannot claim a binding origin: it must omit project/task
and stores both as null.

Thread create/invite, their journal events, roster projection, and idempotency
commit in one transaction. A stale expected participant revision or failed
eligibility check commits none of them. The current context packet's
existing bounded inbox shape can contain an authorized participant message, while
the full body remains an explicit MCP read.

## Curator policy and derivation

`curator_rules` stores immutable workspace policy revisions for the one implemented
`accepted_meeting_resolution_copy/v1` rule. Every workspace is seeded with disabled
revision one. Owner configuration appends a revision; it does
not rewrite history. The effective rule is the highest revision.

`curator_derivations` is append-only proof that one exact rule version copied an
accepted `meeting_proposal` revision into one exact proposed knowledge revision.
It freezes the rule row, workspace/project, source identity/revision and source
hash, output revision/hash, event cursor, time, and `subsystem:curator` actor.
Constraints recheck the project-wide `decision`, exact agenda/summary,
`medium`/`supported`/`until_superseded` metadata, single primary source, and
proposed/pending state at insertion. One source revision and one knowledge
revision can each have at most one derivation.

`curator_auto_acceptances` is immutable explanation evidence for the distinct
state-policy governance path. It links the effective enabled evaluation-rule
revision to a compatible derivation (which may have been created under an earlier
disabled configuration revision), exact knowledge revision, allowed
`subsystem:curator` authority check, ordinary `knowledge.accepted` event, and
`curator.auto_accepted` event. The generic knowledge governance path still denies
subsystem actors.

The curator queue has no table. It is a deterministic projection over proposed,
pending canonical revisions plus derivation and effective-rule rows. Processing
is one idempotent transaction bounded to 100 candidate evaluations and ten
automatic acceptances. Preexisting safe derivations are evaluated first; when
capacity remains, a new exact derivation and its automatic acceptance may commit
together in that transaction. A structured source outside the exact title/body
bounds is returned as a skip evaluation and creates no marker, proposal, or event.

## Knowledge contradictions

`knowledge_contradictions` is immutable exact-pair history plus a narrow lifecycle
projection. It stores one lexically ordered, globally unique pair of different
knowledge items; workspace/project, proposed report reason/actor/time, state
revision, and exact lifecycle event links. Optional confirmation, dismissal, and
automatic-resolution columns are legal only in their corresponding states.

Insert triggers independently require both participants to be accepted/current in
one project with intersecting applicability, the exact `contradiction.detected`
event/actor link, valid encoded UTF-8 bounds, and either `local-owner/human` or a
`starting|active|blocked` reporter run whose exact project/task can see both
participants. Update triggers make identity/report immutable, revalidate
participants at confirmation, require exact owner authority/event linkage for
confirm/dismiss, and require a matching normal knowledge governance event and
authority record for stale/supersede resolution. Deletes and illegal transitions
abort. Direct SQL therefore cannot bypass the Go authority contract.

`knowledge_contradiction_authority_checks` is append-only evidence for every
confirm/dismiss attempt that reaches domain governance. It freezes action, actor,
allowed/denied outcome and reason, optional bounded note, command key/hash, event
sequence, and timestamp. A trigger requires the matching contradiction state and
event actor/revision. Detail queries return a separate total count plus the newest
at most 200 checks; the sample never silently claims to be complete.

Effective dispute has no table and adds no knowledge currency value. It is a
relational projection over open contradiction rows: at most 200 sorted IDs plus a
separate total count. Search applies the same open-row exclusion before `LIMIT`.
Context-build conflict checks fetch only 16 sorted IDs plus a total count and
commit no packet/event/key on failure. Mark-stale and successor acceptance resolve
all incident open rows within their existing knowledge transaction.

## Portable project knowledge

The current baseline includes immutable task-scope anchors and owner import receipts. A
task anchor binds an exact opaque task ID to workspace/project applicability
without requiring or creating an operational task row. Native proposals
validate or create the same binding.
Insert/update triggers prevent an existing or later operational task with that ID
from disagreeing on workspace, project, creation time, or creator identity.

Import receipts freeze bundle/content/rendering digests, exact scope, imported
time/actor, and completion event. Narrow staging/attestation rows allow a validated
final knowledge or contradiction snapshot to be inserted without replaying a
foreign event stream. Triggers accept that path only inside the matching owner
import transaction. Receipt, anchors, all canonical rows, per-entity import
events, and the completion event commit or roll back together; exact byte replay
is recognized from the immutable receipt rather than reconstructed foreign
idempotency state.

The portable bundle itself excludes authority-check and curator tables,
idempotency keys, FTS tables/metadata, context, messages, tasks, meetings, agents,
runs, repositories, checkouts, provider state, credentials, and transcripts.
These tables remain local and are not reconstructed from descriptive actor fields
in an imported snapshot.

## Live context deltas

Current-packet construction includes its source
event high-water, bounded reverse dependents and participant rosters, collaboration
budget, and frozen live policy. Insert triggers require redundant packet
scope/hash/size columns to match the JSON and validate the exact policy bounds.
Context packets and run-context bindings reject update/delete.

`run_context_delta_state` is one durable delivery projection per current-packet run. It
owns `ready|pending_ack|rebase_required`, optimistic revision, inspected event
cursor, last/pending/acknowledged IDs and run-local sequence, delta count,
cumulative bytes, stable rebase reason/event, and timestamps. Initial state must
match the exact immutable run binding and packet `as_of_event_sequence`. Update
triggers permit only four transitions: event-free no-op cursor advance, one ready
chain head becoming pending, exact pending acknowledgement returning to ready, or
ready state becoming durable rebase. State cannot be deleted.

`context_deltas` stores immutable canonical delta JSON plus redundant run/base,
sequence/parent, exclusive/inclusive cursor interval, content hash, byte size,
build event sequence, and audit fields. IDs use `cdelta_...`; run/sequence and
build events are unique. Insert triggers require the row to extend the exact ready
chain, stay under the 64 KiB cumulative limit, match every redundant JSON field,
and link an exact `context_delta.built` journal fact. A row is limited to 16 KiB.

`context_delta_acknowledgements` stores one immutable `cdack_...` receipt per
delta: exact run/base/delta/sequence, authenticated run actor, key/request hash,
time, and unique acknowledgement event. A trigger requires the delta to be that
run's sole pending head and the actor to equal the run. The resulting state update
requires the receipt already exist, preventing owner or cross-run consumption
claims. Exact replay reads the receipt and changes no row/event.

Refresh uses an immediate transaction. Pending/rebase state is returned before a
scan. Ready refresh reads a global journal cutoff, caps potentially applicable
events at 1,000, reloads exact canonical projections, and either atomically stores
one whole delta/event/state transition, advances the no-op cursor without an
event, or records rebase event/state. Event payloads are candidate/audit data, not
the stored delta's content authority.

## Manager delegation and deterministic supervision

The current baseline includes owner-granted manager proposals and a deterministic local
supervisor. `manager_grants` binds one exact workspace/project/objective,
planning-task revision, agent revision, proposal-kind set, target launch-profile
revision set, allowed claim-kind set, quantitative limits, optional expiry, and
content hash. The authority-bearing sets are normalized into immutable
`manager_grant_proposal_kinds`, `manager_grant_launch_profiles`, and
`manager_grant_claim_kinds` rows. The JSON values on the parent are canonical
mirrors, not a second authority source. A grant may be revoked but not rewritten
or deleted.

`launch_profiles` is the complete owner-authored scheduling eligibility record.
It freezes one project, exact agent revision, runtime/provider, optional checkout,
bounded scenario and hash, assignment lease, capability lifetime, and optional
manager-grant binding. A target profile has no manager grant; a planning profile
is bound to the exact grant that authorizes its manager run. Retirement changes
only lifecycle metadata. `purpose` and the agent definition's arbitrary `role`
are descriptive and never participate in authorization or candidate ranking.

`manager_proposals` stores the run-authenticated proposal envelope, exact grant
and objective revisions, journal high-water, immutable closed action array,
validation issues, and content hash. `manager_proposal_actions` normalizes every tagged-union action
and must exactly equal its corresponding frozen JSON element.
`manager_proposal_submissions` proves the complete action set and submission
event. A pending proposal has no coordination effect. Owner acceptance atomically
revalidates scope, revisions, graph acyclicity, budgets, claim kinds, and exact
launch profiles, then writes typed work mutations and immutable
`manager_proposal_effects`. Rejection writes no work effect.
Completion of the source planning run and release of its assignment do not make
an already-sealed pending proposal undecidable. Acceptance instead proves the
immutable source packet/grant tuple, current frozen source-agent revision,
active/unexpired grant, exact active objective revision, and current referenced
profiles/tasks; it never grants the completed run another tool capability.

Accepted assignment-producing actions create immutable
`task_claim_requirements` and one durable `scheduling_intent` for the exact task,
agent, and launch profile. At most one open intent exists per task. Intent states
distinguish pending/deferred work from an owner-approved-but-not-yet-launched
`run_requested` effect and terminal satisfied/failed/cancelled outcomes. A manual
assignment is rejected while any intent is open, so owner assignment cannot race
accepted manager work.

Intent acceptance begins at `pending`; placement contention may produce
`deferred`, and the atomic schedule transaction advances it to `run_requested`.
A completed run closes it `satisfied`; rejected completion or definitive runtime
failure closes it `failed`; a stopped run closes it `cancelled`. A definite
`start_failed` remains `run_requested` only while another exact policy-bounded
retry is authorized. Retry successors retain the original intent, and only the
latest receipt-linked run may close it. Owner task cancellation closes a
pending/deferred intent immediately, or closes `run_requested` only when that
exact latest retry-chain run is definitively `start_failed`; it appends the exact
local-owner `supervisor.intent_cancelled` fact in the same transaction. Disabling
retry or exhausting/lowering its bound closes a stranded start-failure intent
instead of leaving permanent open work.

`supervisor_policies` is append-only owner policy history. Each revision freezes
global, starting, project, provider, and retry/cooldown limits. New workspaces get
a disabled default. `supervisor_actions` freezes one canonical condition,
condition key, entity revision, policy revision, event cursor, constraint
snapshot, response, and explanation. `approval_requests` gives at most one
owner-decision record per action; decisions use optimistic revisions and cannot
be replayed into a different action. `supervisor_state` stores the durable scan
cursor so restart resumes from committed state. A pass captures a journal cutoff,
classifies closed-union pages of at most 1,000 events, and cannot inspect
projections for effects until the cursor reaches that cutoff. A known partial page
may advance the cursor without effects; an unknown event returns
`unsupported_supervisor_event` without advancing the cursor or committing any
action, approval, scheduling effect, or command receipt. Cursor timestamps remain
strictly monotonic even under a frozen wall clock.

The owner-facing `supervisor.run` persists a successful no-op idempotency result,
which prevents the same key from gaining later effects. Internal daemon no-op
passes remain receipt- and event-idle; they only persist a classified cursor
advance when one is required.

Each scheduling intent also stores an internal classified-event watermark and an
optional retry time. These fields bound ready-queue scans without weakening the
global priority/readiness/ID order: an unchanged deferral waits 30 seconds, while
a fact can wake it early only when it matches the latest sealed primary failing
dimension. The readiness key is derived from immutable ready/dependency facts,
not the task projection's general-purpose `updated_at` field.

An accepted `request_action` is materialized as a `manager_escalation`
supervisor action plus one approval request. Its typed `source_proposal_id` and
`source_action_id` foreign-key pair seals provenance to the exact accepted action;
the frozen requested response, target revision, optional reassign profile, and
reason are revalidated again before an allowed effect.

Only `dependency_ready -> schedule` may be applied automatically, and only when
the effective enabled policy has `auto_schedule`. Optional start-failure retry is
bounded by the exact `0..3` retry limit and cooldown. Blocked, stale, failed,
repeated-failure, over-budget, stop, resume, and reassignment responses require an
approval record before application. Lost or otherwise uncertain runs continue to
consume every applicable capacity dimension until run-first reconciliation
establishes their terminal state; the supervisor cannot free a lease merely to
make a replacement fit.

The schema also freezes the scheduling boundary. `runs.assignment_id` binds the
exact assignment. `run_jobs.origin` distinguishes owner and supervisor work, and
every supervisor-origin job requires an immutable `run_scheduling_receipts` row
linking the run, intent, applied action, exact launch-profile revision, assignment,
task revision, and policy revision. The accepted intent, assignment, run, pending
job, receipt, action transition, event facts, and idempotency result commit before
the worker may call a runtime. An interrupted worker therefore reclaims and
reconciles the same run ID instead of scheduling a second effect.

That receipt is frozen launch authority for the one committed operation, not a
pointer that is re-resolved against mutable eligibility later. Worker claim/start
still proves the exact receipt/job/run, current task state, active assignment
link, and immutable requested event. It deliberately does not cancel that
already committed run because its profile is later retired, its agent is later
disabled or its revision changes, or wall time passes the assignment lease
deadline.
Reserved-run reconciliation keeps the exact assignment from expiring underneath
the operation. Any new placement or fresh retry must revalidate the then-current
profile, agent, assignment, claims, checkout, and policy and seal a new receipt.

An enabled bounded `start_failed` retry creates a different run and fresh
packet/capability while preserving the prior failed run. Its immutable retry
receipt binds prior run, new run, applied action, exact profile/policy,
assignment, and attempt number. The assignment and all required claims must still
be active and canonically unexpired; retry cannot extend or resurrect expired
authority merely because history still contains the original scheduling receipt.
The corresponding action stores `prior_run_id` for the immutable failed run and
`run_id` for the fresh requested run; ordinary actions leave `prior_run_id` null.

The current context packet freezes the complete exact manager-grant snapshot and
adds only the proposal tools allowed by that grant. Insert and
binding checks require the active unexpired grant, exact planning task/agent and
profile revisions, live assignment, canonical target-profile tuples, and exact
tool intersection. A packet without that exact grant has no manager authority,
regardless of role-label changes.

The schema rejects update/delete of immutable evidence and rejects partial direct
SQL that cannot prove the same canonical relationships. Transactions use an
immediate write reservation, so concurrent proposal decisions, supervisor scans,
approval decisions, and scheduling attempts serialize their invariant checks.

## Local-check evidence and authority

Local checks remain separate from agent `runs`. The current baseline includes the
following strict tables:

| Concern | Tables |
| --- | --- |
| Owner command allowlist | `check_definitions`, `check_definition_arguments` |
| Named criteria | `task_check_requirements` |
| Delegated authority | `check_watch_grants`, `check_watch_grant_operations`, `check_watch_grant_definitions` |
| Project handling | `check_policies`, `check_routes` |
| Execution saga | `check_runs`, `check_jobs`, `check_launch_receipts` |
| Outcome and retained output | `check_results`, `check_artifacts`, `check_result_freshness`, `check_requirement_evidence` |
| Delivery | `check_notification_receipts`, `check_route_failures` |
| Inert follow-up | `check_repair_proposals`, `check_repair_decisions`, `check_repair_effects` |

Definitions store an absolute executable and typed limits. Arguments are
contiguous immutable child rows. There is deliberately no definition environment,
stdin, shell string, credential, provider, or MCP configuration. One active task
requirement binds one criterion key and statement to one exact definition content
revision; an active task cannot bind the same definition twice.

One `check_watch_grants` row binds the owner-selected project and exact enabled
agent revision. Its child mirrors contain only the closed operations
`run|inspect|propose_repair` and exact definition revisions. Grant insert and
lifecycle triggers prove those mirrors, scope, bounds, expiry, owner authorship,
revision, and content hash. No grant, route, policy, or execution query joins or
filters on `agents.role` or `launch_profiles.purpose`.

Context-packet insert and binding triggers accept a check-watch grant only when it
is the complete exact active grant and no manager grant coexists. Every agent
check mutation revalidates the live run/capability, current packet, current grant revision/expiry, exact agent/project,
operation, and allowlisted definition before it inserts a request.

`check_runs` has only:

```text
requested -> starting -> running -> finished
```

A partial unique index allows one live run per requirement/checkout. One request
transaction inserts the run, pending job, event, and actor-scoped idempotency
result. A worker may claim an external effect only after one immutable
`check_launch_receipts` row proves the exact job, definition and requirement
revisions, checkout, initial Git observation, effective runtime-spec digest,
source authority, and corresponding event. The check-run ID is the stable direct
runtime operation ID. An existing operation with a different spec digest is a
conflict, not replay.

Each finished run has exactly one immutable `check_results` row with outcome
`passed|failed|timed_out|start_failed|unknown`. Exit and diagnostic authority come
from status-only runtime inspection. Text enters storage only through the bounded,
redacted runtime logs path. `check_artifacts` stores immutable kind, SHA-256,
captured/omitted byte counts, and truncation for private content-addressed blobs.
A reference whose blob is missing or hash-mismatched fails canonical read.

Freshness is append-only and separate from process outcome. Initial state is
`fresh` only for available, identical, nonempty, clean launch/terminal
repository-checkout-HEAD observations. A known HEAD difference is `stale`;
dirty/unavailable/invalid observations are `unknown`. A later fresh inspector
observation of a different HEAD or any dirty tree appends stale. Stale cannot
transition back. Only an originally eligible result with no stale observation
may return from a transient observation-unknown state to fresh.

`check_requirement_evidence` is forced to class `mechanical_check` and the exact
requirement/result/freshness-revision tuple. Each tuple has one immutable link:
the historical initial revision-1 link remains readable, and a later stale
revision records a distinct inconclusive link rather than rewriting the earlier
truth. It cannot update task lifecycle or represent `independent_review` or
`policy_acceptance`. The derived requirement state
`missing|running|verified|failed|stale|unknown` is a read projection; only the
latest exact active-revision passed/fresh result is verified.

Every nonpass result attempts one exact current-task-assignment notification.
Owner routes add an exact agent revision and explicit
`evidence_review|coordination` duty. Receipts freeze route/policy, result or
freshness revision, recipient, and assignment when applicable. A missing current
owner inserts `check_route_failures` as `unroutable` instead of guessing.

Subsystem delivery uses `messages.sender_type='subsystem'`. A check message is
valid only with sender `crewfold-check-worker`, null sender agent/run, a direct
thread, and a matching immutable notification receipt in the same transaction.
Public owner/agent message commands cannot select subsystem provenance.

Repair policy is seeded disabled for every existing and new project. An enabled
revision freezes an exact repair launch-profile revision and open-proposal bound.
A repair proposal is immutable and inert. One owner decision may accept it only
after revalidating the latest exact trusted failed result at the current fresh
source, its authenticated watcher run/agent/grant tuple, policy, task/objective,
and exact current profile; the task, scheduling intent, decision, effects, events, and
idempotency response then commit together. A later fresh pass stales a pending
proposal. The immutable decision freezes the exact proposal revision,
`accepted|rejected` value, optional canonical note of at most 4096 encoded UTF-8
bytes, timestamp, and `local-owner` author; undecided detail has no synthetic
decision row.

Timed-out, start-failed, and unknown outcomes, plus stale or unknown freshness,
remain durable and inspectable but cannot seed a repair proposal.

The schema defines immutable/reject-delete, legal-transition, exact-scope,
canonical-mirror, receipt-provenance, and child-ordinal triggers for each family.
Local-check persistence uses named sqlc queries and generated models; the bounded
supervisor transaction exception does not extend to this surface.

Named fault barriers surround request projection/event/job/idempotency; launch
receipt/event; external launch; handle binding; terminal
result/artifact/freshness/evidence/notification/message/event; and repair
decision/effect. Each database bundle is wholly absent or wholly committed.
Content-addressed blob preparation may leave an unauthoritative orphan but cannot
produce a valid result without its exact typed metadata.

## Atomic command path

`workspace.init` executes one immediate transaction:

1. look up the idempotency key;
2. return the stored result, or reject reuse with another command hash;
3. enforce unique workspace-name and input invariants;
4. insert the workspace projection at revision 1;
5. append `workspace.created` with the same entity/revision;
6. store the successful response under the idempotency key;
7. commit all three writes together.

Failures before commit leave no workspace, event, or idempotency record. Tests
this both by injected rollback errors and by killing a helper daemon process after
the projection write and after the event append. Restart recovers the WAL, proves
all three tables unchanged, and permits the same idempotency key to succeed.

## Recovery and backup boundary

Normal restart reopens and validates the same database before serving requests.
SQLite owns WAL recovery; Crewfold does not interpret or delete WAL/SHM files.

Crewfold does not yet expose backup/restore commands. A later capability must use
SQLite's online backup API for a running database rather than copy the main file
without its WAL. The current database contains agent/task/run/claim coordination,
meetings, canonical knowledge, immutable context packets, scoped
report/artifact/audit records, durable message/thread/delivery/wake state,
overlap/drift/watcher state, bounded curator policy/derivation/acceptance evidence,
exact contradiction history and bounded derived dispute evidence,
opaque fake/direct bindings, direct supervisor references, and a rebuildable FTS
projection, plus portable task-scope anchors, exact owner import receipts, and
per-entity import attestation rows. It contains no provider-private session
transcript. It additionally contains immutable context-delta chains, exact-run
acknowledgement receipts, and durable scan/rebase state. Backup of a live
installation must include a
coordinated snapshot of the database, direct-runtime state, node key, and
capability files; restored capabilities still obey their stored expiry and run
state. It also includes manager grants/proposals, immutable launch profiles,
accepted-action effects and scheduling intents, append-only supervisor policy,
actions/approvals/state, and exact supervisor scheduling receipts. Restoring only
part of that authority graph is unsupported and must fail canonical health or the
first dependent operation closed. It further includes check definitions and
criteria, current-packet check-watch grants, check execution receipts/results/freshness/evidence,
subsystem delivery receipts, and inert repair proposals. A coordinated backup
must include the private content-addressed check-artifact directory and dedicated
direct-check runtime state; missing or hash-mismatched output fails closed rather
than being summarized as verification.
