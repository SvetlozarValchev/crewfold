# Storage contract

Status: implemented as one current greenfield schema baseline.

## Location and ownership

The foreground daemon opens `<data-dir>/crewfold.db` only after it holds the
exclusive `<data-dir>/daemon.lock`. A newly created data directory uses mode
`0700`; the database and lock use `0600`. Data-directory components and the lock
are opened without following symbolic links. An existing lock must already be an
owner-held, single-link, exact-`0600` regular file; Crewfold never chmods or
truncates an unsafe lock. Crewfold likewise refuses a database path that is a
symbolic link, aliased hard link, wrong-owner/mode file, or non-regular file.

Crewfold writes the ASCII application ID `CRFD` into SQLite's file header. An
unidentified database with a nonzero schema marker or user tables is preserved
and refused rather than adopted as Crewfold storage.

The database is opened through the CGO-free `github.com/ncruces/go-sqlite3`
`database/sql` driver. The driver and its transitive build dependencies are
vendored so `GOPROXY=off` builds and tests do not depend on a warm module cache.

## Connection policy

Crewfold uses one serialized writer connection and at most four bounded WAL
reader connections. Every mutation transaction is admitted through the writer;
read-only status, health, and projection snapshots use the reader pool, so an
external writer held through SQLite's busy timeout cannot consume all
control-plane connections. The fixed bounds keep per-connection SQLite settings
and authenticated construction seals unambiguous; there is no unbounded pool.

Every connection requires:

| Setting | Value | Purpose |
| --- | --- | --- |
| `journal_mode` | `WAL` | Durable crash recovery with concurrent readers |
| `foreign_keys` | `ON` | Enforce projection/event relationships |
| `busy_timeout` | 5000 ms | Bound transient lock contention |
| `synchronous` | `FULL` | Favor local durability over mutation throughput |
| transaction lock | writer `IMMEDIATE` | Acquire the write reservation before mutation invariant checks |

Startup fails before the API socket is bound if baseline initialization or canonical database
health checks fail. Crewfold opens a short-lived base SQLite connection without
registering the FTS5 module and runs one global `PRAGMA quick_check(1)`. This checks
database-wide page allocation, the freelist, and every ordinary B-tree—including
the FTS shadow tables—without invoking the disposable virtual table's semantic
`xIntegrity` hook. Physical failure is distinct from retryable `database_busy`;
there is no error-string table-filter fallback. `database.status` reports the
compiled baseline SHA-256, installed `sqlite_schema` SHA-256, journal mode,
foreign-key setting, and global physical result. Retrieval projection semantics
are checked and reported separately.

## Embedded current baseline

The one SQL source `internal/store/baseline/current.sql` is embedded into the
binary. A fresh data directory is initialized transactionally and then verified
against a compiled baseline SHA-256, the fixed `CRFD` application ID, and a
deterministic digest of sorted canonical/control `sqlite_schema`
type/name/table/SQL tuples. The exact rebuildable `knowledge_search` virtual
table, its SQLite-owned shadows/indexes, and `knowledge_search_metadata` are
excluded from that catalog digest and verified by the separate derived-index
check, so SQLite's equivalent FTS DDL rewrite and an explicitly rebuildable
missing index do not masquerade as authority-schema drift. A singleton
`schema_baseline` row stores the compiled hash.

On open, an empty database receives exactly that baseline. Any nonempty database
whose application ID, baseline row, or actual installed-schema digest differs is
preserved and refused with `current_baseline_mismatch` before workers or the
listener start. There is no migration directory, `schema_migrations`,
latest-version comparison, DDL upgrade, same-version adoption, historical import,
or compatibility branch.

A static integrity registry classifies every application table exactly once as
canonical domain state, durable control/receipt/queue state, or rebuildable
derived state. It also declares terminal quiescence states for every durable
external-effect queue. Full health and backup verification scan every registered
row; a baseline object missing from the registry is itself an integrity failure.

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

### M22 domain membership, sessions, and staffing

`domain_agent_memberships` places a workspace agent definition in one Project
presented as a domain. The optional parent is another active membership in that
same domain; optional workstream is an Objective in that Project. Triggers and
Store transactions keep the relation acyclic and same-domain. Parentage,
preferred entry, name, and role organize owner attention only and grant no
authority. One domain may have zero, one, or many roots and arbitrary bounded
depth; `domain-steward` has no special storage meaning.

`domain_agent_session_bindings` binds the durable agent identity to one private
Codex provider thread and current node fingerprint. Public results expose only
whether a conversation exists, its bounded readable turns, current thread state,
and selected checkout directory. Thread IDs, node identity, node fingerprint,
raw reasoning, credentials, and capability material never cross the public JSON
boundary. A provider process may exit between turns; the durable binding resumes
the same thread when possible and records an explicit detached state otherwise.

`domain_agent_tool_receipts` makes each dynamic-tool call replay-safe by exact
agent/session revision, private call+turn identity, tool, argument hash, response
hash, status, and bounded response. It never turns provider output into canonical
knowledge implicitly.

Owner-authored authority for descendant creation is normalized across
`domain_agent_staffing_grants`, `domain_agent_staffing_profiles`, and
`domain_agent_staffing_task_classes`. Every successful child atomically inserts
its agent definition, same-domain membership, immutable
`domain_agent_staffing_allocations` receipt, and three events. The grant's manager
membership revision, status/expiry, provider/runtime profile, task class,
descendant ceiling, cumulative concurrency, and token/cost/time allocation are
checked in that same transaction. An expired grant is reported as expired at the
read boundary without appending an event; it cannot authorize a child. A revoke
racing creation cannot leave a definition without membership or allocation.

## Runs and deterministic execution

The task state constraint includes `review`, `changes_requested`, and `failed`
for evidence-driven run outcomes.

`runs` stores committed execution intent, task/agent/checkout placement, opaque
runtime/provider names, the validated fake scenario, normalized
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
its assignment, checkout, or capacity and prevents a quiescent backup.

Opaque runtime/provider identities live only in internal
`run_runtime_bindings`/`check_runtime_bindings`, bound to the current node-key
fingerprint and exact launch operation. Public run/check projections and event
payloads contain no handle. A binding is legal only while its operation is live
and is cleared by terminalization; a normal terminal row retaining one fails
canonical integrity. Direct supervisor files remain owner-private operational
state. They are atomically replaced, excluded from backup, and removed after the
terminal projection and retained-log metadata commit.

The current table boundary is exact:

| Table | Current fields and invariant |
| --- | --- |
| `runs` | canonical run lifecycle and placement only; no runtime/provider handle column |
| `check_runs` | canonical check lifecycle only; no runtime handle column |
| `run_runtime_bindings` | one live `run_id`, node fingerprint, launch operation ID, runtime handle, optional provider handle, and timestamps; delete on terminal transition |
| `check_runtime_bindings` | one live `check_run_id`, node fingerprint, launch operation ID, runtime handle, and timestamps; delete on finished transition |
| `run_log_artifacts` | one `stdout|stderr` row per terminal run with `captured|unavailable` state, captured/omitted bytes, truncation, nullable content SHA-256, and bounded diagnostic code |

The binding tables are internal durable crash-reconciliation state, not domain or
event projections. The current node fingerprint must match on every control or
reconciliation operation. No terminal row can regain a binding.

Before normal terminalization clears a run binding, Crewfold reads, redacts, and prepares
at most 64 KiB each of stdout and stderr as immutable content-addressed files at
`run-artifacts/<first-two-hex>/<sha256>`. Database metadata preserves captured and
omitted byte counts, truncation, content hash, and captured/unavailable state.
Live `run.logs` reads the node binding; terminal `run.logs` reads only those
artifacts. An untrusted lost outcome records explicit unavailability rather than
an empty successful log. Full Herdr buffers/transcripts and process identity are
never historical log artifacts.

There is deliberately no separate run-log archive queue. Log capture is a
read-only adapter step before terminalization; the terminal transaction commits
the final run projection, settled run job, binding deletion, and immutable-log
references together. A crash before that transaction leaves the live binding
and job retryable, while a committed terminal row already has its exact captured
or unavailable log receipt.

## Context packets and run-scoped MCP

`context_packets` stores an immutable bounded JSON packet, semantic SHA-256 hash,
byte size, task/agent/checkout scope, and creation provenance. The packet includes
its own exact entity revisions and selection/exclusion explanation. Each
`run_context_bindings` row binds one packet to one run; both sides are unique.

`run_capabilities` stores only expiry—not the credential. A private node key under
daemon state derives per-run HMAC tokens, and private token files give direct
children capability access without putting secrets in SQLite or public launch
records. The key and tokens are node-bound operational authority and are excluded
from quiescent backups.

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
the message and signals a dedicated worker; the request handler never calls a
runtime. The worker handles one external wake at a time, at most 16 per pass, with
a five-second call deadline and no open database transaction. Wake is at-most-
once: startup turns a previously executing, outcome-unknown call into
`failed_unknown` rather than sending a duplicate prompt. Success may advance a
still-queued delivery to delivered. Failure/unknown stores a bounded diagnostic
and leaves delivery queued so inbox polling remains authoritative.

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

## Owner workbench conversations

`owner_conversations` is the bounded workspace/project thread shown by the local
web workbench. `owner_turns` freezes each owner instruction or proactive
executive review at one event high-water. It stores the bounded response,
selected citations, origin, trigger cut, idempotency hash, and exact execution
state. A turn is an audited conversation envelope and never grants authority by
itself.

`owner_executive_bindings` records the one immutable current authority tuple for
a project executive: project-direction objective, standing assigned planning
task, exact agent, owner-authored manager grant, and selected management launch
profile. The insertion trigger proves that every member is current and belongs
to the same workspace/project. Role and purpose strings are not inputs.

`owner_executive_exchanges` is the bounded durable queue joining one turn to its
binding, frozen canonical context, citation namespace, short-lived run, and
linked typed manager proposals. Claim, dispatch, response, retry, and failure
states are explicit. Provider output cannot directly populate objectives,
tasks, dependencies, scheduling intents, or approvals.

`owner_manager_review_jobs` coalesces worker-originated events by project. A
leased pass freezes a current event cut, queues an executive review turn, then
records the reviewed cut. Events arriving during that process advance the
requested cut and cause a later bounded pass rather than being lost.

The closed `owner_turn_operations`/`owner_effect_receipts` storage envelope is
structurally validated, but it is not an owner-facing M21 path.
The current browser accepts or rejects executive-created `manager_proposals`;
proposal validation and application use the existing typed manager proposal
operations and receipts. Objectives, tasks, dependencies, scheduling intents,
runs, approvals, proposals, and events remain the sole domain truth.

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

## Owner outcomes, checkpoints, and briefing projection

`deliverable_commitments` stores one immutable owner promise at an exact
workspace/project/objective/task scope. Its ordered acceptance criteria and
canonical content hash are frozen at creation; `(task_id, commitment_key)` is
unique. `outcome_commitment_receipts` binds every commitment to the one durable
creation event.

`outcome_assessments` is the revision stream for one commitment. Partial unique
indexes permit at most one `proposed` row and one current `accepted` row per
commitment. The only mutable columns are the sealed governance transition from
proposed to accepted/rejected and the sealed accepted-to-superseded transition.
All content and scope columns remain immutable. A successor acceptance updates
the old current row, the successor, governance receipts, acceptance basis,
events, and the idempotency response in one immediate transaction; rejection
does not alter the current accepted row.

Typed bounded child tables retain ordered decision revisions, evidence sources,
compatibility/stability effects, deviations, risks, unknowns, follow-up tasks,
and owner attention. Caller evidence is constrained to exact handoff or check-
requirement-evidence records. The Store derives and freezes source revision,
hash, event sequence, evidence class/effect, and pinned freshness; current
freshness and dispute diagnoses remain read-time derived facts. Submission,
governance, and acceptance-basis receipts make event and authority provenance
exact.

`owner_checkpoints` stores an immutable task, objective, project, or workspace
scope with its exact event sequence, creation time, and `local-owner` identity.
There is no checkpoint lifecycle column.

`outcome_projector_state` is the per-workspace restart cursor. The projector
reads the closed event union and advances this cursor in transactions covering at
most 1000 events. Outcome assessment commits and briefing materialization are
separate transactions from those cursor pages. An unknown event fails closed and
records the diagnostic without interpreting the payload. `management_briefings`
stores the exact scope, captured current high-water, optional checkpoint lower
bound, evaluation time, caught-up diagnosis, canonical content hash, and byte
size. Briefing rows contain at most 64 KiB of canonical JSON.

`management_briefing_claims` stores at most 128 whole ordered claims. Every
`claim_id` is `bclaim_` plus the lowercase SHA-256 of canonical scope, semantic
kind, exact ordered sources, and status. `management_briefing_claim_sources`
stores the ordered exact entity revisions, hashes, and event sequences used by
each claim. Omission counts remain inside canonical briefing content and are
aggregated by the seven closed sections and `claim_limit|byte_limit` reason.
Reads and explanations append no event.

Outcome writes are protected by a dedicated Store mutation seal and current-
schema triggers reject direct inserts, illegal transitions, child ordinal gaps,
scope/provenance mismatches, updates to immutable records, and deletes. The
projector uses a sealed Store-only write path for derived projection state. The
seal does not extend caller or agent authority.

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

### Captured quiescence

`backup.create` uses SQLite's online backup API to create a standalone database
snapshot while the daemon remains responsive. Quiescence and integrity are
evaluated against that captured database, never a later live read. Rows committed
after the cut are not part of the backup.

The snapshot is quiescent only when:

- no run is `requested`, `starting`, `active`, `blocked`, `stopping`, or `lost`;
- every run job is settled and no run binding exists;
- every check run is finished, every check job complete, and no check binding
  exists;
- no message-wake job is pending, leased, or executing;
- no scheduling intent is `pending`, `deferred`, `awaiting_approval`, or
  `run_requested`;
- no supervisor action is proposed, awaiting approval, or deferred;
- no approval is pending or granted but unconsumed; and
- every other integrity-registry external-effect queue is in one of its declared
  terminal states.

Enabled supervisor policies, active grants without a live run/capability, inert
owner proposals, and task assignments do not themselves block a cut. They are
durable configuration or coordination state, not an in-flight effect. A `lost`
run does block because the external process may still write; only the owner's
explicit native-runtime retirement and `run resolve-lost` mutation can clear it.

### Exact bundle

The bundle directory contains only:

```text
manifest.json
manifest.sha256
crewfold.db
check-artifacts/<first-two-hex>/<sha256>
run-artifacts/<first-two-hex>/<sha256>
```

`crewfold.db` is the standalone online-backup result, never a copy of the live
main file. Artifact enumeration comes from the snapshot; only exactly referenced
immutable check and terminal-run-log content is copied. Artifact hash or absence
fails creation.

The bundle excludes `crewfold.db-wal`, `crewfold.db-shm`, lock/socket/PID files,
maintenance receipts and staging, `node.key`, capability tokens, active
`runtime/` and `check-runtime/` state, Herdr sessions/transcripts, provider homes
and credentials, source repositories/checkouts, daemon logs, and unreferenced or
orphan artifacts. The database itself remains sensitive because canonical rows
include messages, evidence, and registered checkout paths; backup is not a
redacted export.

Canonical `manifest.json` has schema
`urn:crewfold:schema:backup-manifest:v1` and records:

- `backup_<32-lower-hex>` ID and canonical creation time;
- exact compiled-baseline, installed-schema, and canonical-plus-durable logical-
  state SHA-256 values;
- captured event high-water and quiescence proof counts;
- a top-level `database` object with relative path, size, and SHA-256;
- a bytewise relative-path-sorted entry list whose records contain path, closed
  kind (`check_artifact|run_log_artifact`), integer mode, size, and SHA-256; and
- total entry count and bytes, counting the database plus artifacts. The manifest
  envelope files are not entries.

`manifest.sha256` hashes the canonical manifest bytes. Bundle directories use
integer mode `448` (`0700`) and files use integer mode `384` (`0600`).
Verification rejects absent or extra entries,
non-normal relative paths, traversal, symlinks, hard-link aliasing, devices,
FIFOs, sockets, non-regular files, unsafe modes, size/hash mismatch, unknown
manifest/baseline, logical-state mismatch, and full-integrity failure. Safety
bounds are a 32-MiB manifest, 200,000 entries, 4,096-byte relative path, 8-GiB
database, and 16-GiB complete bundle. Verification streams entry content.

These private hashes detect corruption and inconsistent copy. They do not prove
authenticity against a malicious same-UID process that can rewrite both manifest
and contents. M20 adds no encryption or signature claim.

Creation uses a private sibling staging directory and publishes through one
rename after every check succeeds. Cancellation, daemon kill, disk exhaustion,
or a changed source artifact before that rename leaves the requested target
absent. Failure or process loss after the rename but before the receipt or
response may leave the fully verifiable target in place; it never exposes a
partial directory. A private source maintenance receipt, excluded from the
bundle and journal, binds target/request hash to the idempotency key; replay after
a lost response reconciles and returns that same complete result. Maintenance
emits no coordination event.

Creation rejects a target equal to or nested below the source data directory
before creating a receipt, lock, or staging entry. Restore similarly rejects a
target equal to or nested below its source bundle before mutating the bundle or
target parent. A publication target cannot use Crewfold's reserved recovery
parent-lock name. Every externally selected recovery source, bundle, repair or
activation data directory, and publication target is rejected if any path
component matches the reserved recovery staging grammar. Internal unpublished
bundle verification bypasses only that public-path check after the durable intent
has established Crewfold's ownership. Component-aware checks still permit sibling
paths that merely share a string prefix. Thus selected source, bundle, restored,
activated, and repair data cannot be reclaimed as abandoned recovery staging.

The private parent lock contains one canonical, fsynced staging intent naming the
exact operation kind, target, and deterministic stage. Recovery never enumerates
or deletes siblings merely because their names resemble the reserved grammar. It
recursively removes a leftover stage only when that valid parent intent proves
Crewfold created it; a stale marker-owned prior operation may be cleaned under the
same parent lock, while an unmarked, malformed, unsafe, or mismatched collision is
preserved and the new operation fails closed.

### Offline verification, restore, and activation

`backup verify <bundle-dir>` needs only the bundle and current binary. It checks
every manifest, file, SQLite, baseline, event, projection/receipt, queue,
quiescence, artifact, and logical-state invariant without contacting or mutating a
source installation.

`backup restore <bundle-dir> --to <new-data-dir>` repeats verification, builds a
private sibling staging directory, copies the standalone database and referenced
artifacts, creates `daemon.lock` plus a sealed `.restore-pending.json`, and
publishes by rename. The target must not exist. Restore creates no node key,
capability, runtime/check-runtime state, or coordination event and does not change
the captured event cursor.

A daemon presented with a pending target returns `restore_not_activated` before
database recovery, capability initialization, workers, runtime drivers, socket,
or listener. Activation requires:

```text
crewfold backup activate <new-data-dir> --confirm-source-retired
```

Under the target lock, activation rechecks the exact baseline, full integrity,
manifest-derived logical state, referenced artifacts, and complete quiescence
predicate. It records the explicit disaster-recovery assertion, generates a new
`node.key`, creates empty capability/runtime/check-runtime roots, and seals the
new node fingerprint and activation digest without changing domain rows or the
event cursor.

The first daemon startup validates that seal and quiescence before any mutation
or external call. Added nonterminal work or a binding returns
`restore_unsafe_nonterminal`. The source need not remain available; source
retirement is an explicit owner assertion. Excluding secrets and bindings ensures
the target cannot control a source runtime, while the retirement assertion
prevents two installations from launching fresh work against the same checkout.

### Offline repair inspection

`repair inspect <data-dir>` opens the existing `daemon.lock` without following a
symlink and takes a nonblocking exclusive flock. A live owner returns
`repair_source_in_use`. Inspection copies database/WAL/SHM bytes to an owned
temporary directory before SQLite recovery or checks, so it mutates no selected
source byte. It can therefore diagnose a baseline or canonical failure that
prevents daemon startup.

Stale repair scratch cleanup likewise requires an exact private root, canonical
fixed ownership-marker bytes in its nofollow single-link file, and a successful
nonblocking lifetime flock. Unmarked root/stage grammar matches, fake markers,
unsafe modes, and live roots are never deleted; a crash before marker publication
may leave an inert temporary directory rather than risking unrelated owner data.

Repair only reports bounded stable findings and one of: retry, rebuild the
derived knowledge index, retire a lost runtime, free space, restore a verified
backup into a new directory, or report a defect. It performs no canonical edit,
salvage, migration, vacuum, reindex, orphan deletion, or automatic repair.
