# Storage contract

Status: implemented through schema version 4.

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

Startup fails before the API socket is bound if migration or database health
checks fail. `database.status` reports the schema version, journal mode,
foreign-key setting, and `quick_check` result.

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
without its WAL. Schema version 4 contains agent/task/run coordination and opaque
fake-adapter bindings but no message, knowledge, real provider-session, or real
runtime-process state.
