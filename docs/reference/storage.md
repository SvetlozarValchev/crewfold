# Storage contract

Status: implemented through schema version 12.

## Location and ownership

The foreground daemon opens `<data-dir>/crewfold.db` only after it holds the
exclusive `<data-dir>/daemon.lock`. A newly created data directory uses mode
`0700`; the database and lock use `0600`. Crewfold refuses a database path that is
a symbolic link or a non-regular file.

Crewfold writes the ASCII application ID `CRFD` into SQLite's file header. An
unidentified database with a nonzero schema version or user tables is preserved
and refused rather than adopted and migrated accidentally.

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

Startup fails before the API socket is bound if migration or canonical database
health checks fail. Crewfold opens a short-lived base SQLite connection without
registering the FTS5 module and runs one global `PRAGMA quick_check(1)`. This checks
database-wide page allocation, the freelist, and every ordinary B-tree—including
the FTS shadow tables—without invoking the disposable virtual table's semantic
`xIntegrity` hook. Any failure remains `storage_failed`; there is no error-string
classification or table-filter fallback. `database.status` reports this global
physical/canonical result alongside schema version, journal mode, and the
foreign-key setting. Retrieval projection semantics are checked and reported
separately.

## Embedded migrations

Ordered SQL files under `internal/store/migrations/` are embedded into the binary.
Names begin with a contiguous three-digit version. Each migration and its
`schema_migrations` record commit in one transaction, and SQLite `user_version`
tracks the current binary schema.

The daemon refuses a database whose version is newer than the binary. Every
supported starting version has checked-in fixture data under
`internal/store/testdata/`. Base fixtures plus representative coordination upgrade
records prove every forward migration while preserving existing records.

## Schema version 1

`workspaces` is the current-state projection. It stores an opaque stable ID,
unique human name, revision, timestamps, and creating/updating actor IDs.

`events` is the immutable local journal. Its integer primary key is the strictly
increasing local cursor. Each row also has an opaque event ID, event type/schema,
two timestamps, actor, workspace, entity/revision, correlation/causation IDs, and
validated JSON data.

`idempotency_keys` stores the command name, canonical request hash, and exact
successful domain result. Keys are globally unique within this local database in
M2. Retention/compaction is deferred until command volume makes it necessary.

`schema_migrations` records the ordered migrations applied to the database.

## Schema version 2

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

## Schema version 3

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

## Schema version 4

The task state constraint expands with `review`, `changes_requested`, and `failed`
for evidence-driven run outcomes. The migration rebuilds tasks, dependencies, and
assignments while preserving their IDs, revisions, edges, leases, and audit data;
upgrade records contain representative completed and actively assigned tasks.

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

## Schema version 5

Run state adds `stopping`, `stopped`, and `lost`, plus bounded stop-grace and
forced-stop facts. The migration rebuilds the run-owned tables in dependency order
and preserves existing run intents, handles, queues, timelines, and handoffs.

The live-run uniqueness and checkout-capacity indexes include `stopping` and
`lost`. A lost process may still be writing, so uncertainty cannot silently free
its assignment or checkout. Direct supervisor files live under owner-only daemon
state rather than SQLite; the database stores only the opaque runtime handle and
coordination meaning. Each supervisor state file is atomically replaced and
contains process identity, exit/timeout/stop result, output byte counts, and an
explicit unknown state when identity cannot be verified.

## Schema version 6

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

Old runs migrate without invented packet bindings or capabilities; only runs
created under schema version 6 receive them. Capability expiry and terminal run
state are both checked on each MCP request.

## Schema version 7

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
project; owner inspection is wider but does not mutate delivery state. Existing
schema-version-6 databases migrate with empty message tables and no fabricated
threads, mail, recipients, or wake work.

New context packets use domain schema v2 because adding the inbox snapshot is an
incompatible wire change. Stored v1 packets remain readable, retain their original
byte size and semantic hash, omit the v2 inbox, and do not acquire the new mailbox
tools. Local `context.show` accepts either version; `context.build` emits v2.

## Schema version 8

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

## Schema versions 9 and 10

Schema version 9 adds frozen structured meetings, participant checkpoints,
independent contributions, typed proposals/actions, and authority/application
records. Meeting resolution commits its complete authorized action set atomically.

Schema version 10 adds stable knowledge items, immutable-content revisions,
ordered frozen provenance, and append-only governance-authority records. Database
constraints permit only proposed acceptance/rejection, current staleness, and
atomic predecessor supersession. It also enforces update/delete rejection for
stored immutable context packets. Context packet v3 embeds exact accepted revision
snapshots; the packet remains canonical even if later governance changes.

## Schema version 11

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

## Schema version 12

`message_threads.kind` distinguishes existing `direct` threads from
`participant_bound` collaboration, and `participant_revision` provides optimistic
roster concurrency. Existing rows migrate as direct threads at participant
revision zero; no cross-project authority is inferred for old mail.

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
retain the schema-version-7 project rule. Message bodies stay immutable and retain
the authenticated sender run's origin project/task.
Owner participant mail cannot claim a binding origin: it must omit project/task
and stores both as null.

Thread create/invite, their journal events, roster projection, and idempotency
commit in one transaction. A stale expected participant revision or failed
eligibility check commits none of them. Context packet v3 is not revised: its
existing bounded inbox shape can contain an authorized participant message, while
the full body remains an explicit MCP read.

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
without its WAL. Schema version 12 contains agent/task/run/claim coordination,
meetings, canonical knowledge, immutable context packets, scoped
report/artifact/audit records, durable message/thread/delivery/wake state,
overlap/drift/watcher state, opaque fake/direct bindings, direct supervisor
references, and a rebuildable FTS projection. It contains no provider-private
session transcript. Backup of a live installation must include a
coordinated snapshot of the database, direct-runtime state, node key, and
capability files; restored capabilities still obey their stored expiry and run
state.
