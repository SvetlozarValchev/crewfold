# Storage contract through M3

Status: implemented through M3.

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
supported starting version has a checked-in fixture under
`internal/store/testdata/`; M2 introduced the version-zero fixture and migration
to schema version 1. M3 adds a version-one fixture and migration to version 2.

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

## Atomic command path

`workspace.init` executes one immediate transaction:

1. look up the idempotency key;
2. return the stored result, or reject reuse with another command hash;
3. enforce unique workspace-name and input invariants;
4. insert the workspace projection at revision 1;
5. append `workspace.created` with the same entity/revision;
6. store the successful response under the idempotency key;
7. commit all three writes together.

Failures before commit leave no workspace, event, or idempotency record. M2 tests
this both by injected rollback errors and by killing a helper daemon process after
the projection write and after the event append. Restart recovers the WAL, proves
all three tables unchanged, and permits the same idempotency key to succeed.

## Recovery and backup boundary

Normal restart reopens and validates the same database before serving requests.
SQLite owns WAL recovery; Crewfold does not interpret or delete WAL/SHM files.

M3 does not yet expose backup/restore commands. A later milestone must use
SQLite's online backup API for a running database rather than copy the main file
without its WAL. Schema version 2 contains no agent, task, message, knowledge,
runtime, or provider state.
