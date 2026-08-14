# M2 milestone review — Persistent workspace and event journal

## Identity

- Milestone: `M2 — Persistent workspace and event journal`
- Review status: `passed`
- Implementation commit: `a705cbd641fbaf316d31dc7015eb8901f1e9e01d`
- Reviewer: Crewfold local implementation gate
- Date: `2026-08-12`

## Demonstrable outcome

- User-visible capability: initialize one durable workspace, inspect it by name or
  stable ID, inspect its immutable creation event from a resumable cursor, check
  SQLite health, stop the daemon, restart it against the same data directory, and
  retrieve byte-identical state.
- Acceptance scenario path: `test/scenarios/persistent-workspace/run.sh`
- Exact command: `./scripts/check.sh`
- Expected result: static, unit, race, schema, component, and all M0–M2
  black-box checks pass; the persistence scenario prints `Persistent workspace acceptance: PASS`; no daemon
  or socket remains.
- Observed result: passed on Linux/amd64 with Go 1.26.5 and SQLite provided by the
  vendored CGO-free `github.com/ncruces/go-sqlite3` v0.35.3 driver.

## Test evidence

| Suite | Command | Result | Evidence |
| --- | --- | --- | --- |
| Formatting/static | `./scripts/check.sh` | passed | First-party Go formatting clean; `go vet ./...` passed |
| Unit/store | `go test ./...` via check script | passed | CLI, store, current baseline, daemon, protocol, and prior packages |
| Race | `go test -race ./...` via check script | passed | Store, daemon, and subprocess crash harness are race-clean |
| Store/schema | `go test ./internal/store` | passed | Fresh current schema, baseline metadata, foreign-schema refusal, pragmas, permissions, identity, invariants, and restart |
| Protocol | `go test ./protocol` | passed | Unique valid schemas, resolved references, published IDs, and nested actor/entity event envelope |
| Component | `go test ./internal/daemon` | passed | API, database doctor, idempotency, pagination, restart, startup failure, and forced process-death recovery |
| Black-box acceptance | Persistent workspace scenario via check script | passed | `Persistent workspace acceptance: PASS` after create/replay/query/stop/restart/restore |
| Earlier milestones | Build and daemon scenarios via check script | passed | `Buildable repository acceptance: PASS`; `Daemon API spine acceptance: PASS` |
| Clean module cache | `GOMODCACHE=<empty> GOPROXY=off go test -count=1 ./...` | passed | Vendored dependency makes a cold offline test deterministic |
| CGO independence | `CGO_ENABLED=0 GOPROXY=off go test -count=1 ./...` | passed | SQLite path does not require a C toolchain or system SQLite |
| Repetition | `go test -count=10 ./internal/store ./internal/daemon` | passed | Store/lifecycle/crash tests passed repeatedly |
| Scenario repetition | Five consecutive persistent workspace scenario runs | passed | No lifecycle, persistence, or cleanup flake |
| Live conformance | N/A | M2 has no runtime/provider/remote service | No network, model, or credential invocation |

## Failure proof

- Injected failure: pause a real helper daemon after inserting the workspace
  projection and, separately, after appending the event; kill the process at each
  named transaction barrier before the idempotency record and commit.
- Injection seam/barrier: `after_projection` and `after_event` hooks in the store,
  reached through a real Unix-socket `workspace.init` request in a child process.
- Expected diagnosis and recovery: the client connection fails because the daemon
  dies; restart performs SQLite WAL recovery; no projection, event, or idempotency
  record is visible; the same idempotency key remains usable.
- Observed diagnosis and recovery: both child processes were forcibly terminated,
  both restarts reported no workspace and an empty journal, and both accepted the
  original key as a new successful command.
- Operation/event IDs: successful commands return an opaque workspace ID, event
  ID, and event sequence. The request ID is persisted as the event correlation ID.

Additional negative cases reject a duplicate workspace name, changed payload
under an existing idempotency key, invalid name, future/tampered schema, database
symlink, and unrelated SQLite database. These failures append no domain event and
do not mutate user-owned files.

## Persistence and recovery

- Durable state exercised at the reviewed M1 commit: `workspaces`, append-only
  `events`, `idempotency_keys`, and the then-current `schema_migrations`
  metadata; SQLite's file header carries application ID `CRFD`. M20 superseded
  that historical layout with one exact `baseline/current.sql` and
  `schema_baseline` identity; the current product has no migration ladder.
- Restart/crash points tested: graceful daemon restart after a committed command;
  forced process death after projection insertion and after event insertion.
- Reconciliation outcome: committed workspace/event/idempotency state returns
  byte-identically after restart; uncommitted transactions are wholly absent.
- At this reviewed commit, fresh-database initialization applied the embedded
  baseline and verified it at startup. The current M20 implementation creates and
  verifies its single exact baseline atomically.
- At this reviewed commit, online backup was deferred. M20 now uses SQLite's
  online backup API and still forbids copying only an open main database file.

At the reviewed M1 commit SQLite used one bounded connection. The current M20
contract keeps one serialized writer plus a bounded four-connection WAL reader
pool, foreign keys, a 5000 ms busy timeout, `synchronous=FULL`, immediate write
transactions, startup quick validation, and exact baseline identity. Database,
lock, event, and idempotency writes still use one local authority; there is no
distributed state.

## Security and autonomy

- New actions/capabilities: create and read Crewfold-owned database state within
  the explicitly selected data directory; answer local workspace/event queries.
- Allowed/denied scope: the owner-only socket may initialize valid workspace names
  and query state. Duplicate/invalid commands, mismatched idempotency keys,
  unsupported schemas, non-regular/symlink database paths, and unidentified
  databases containing user tables are denied.
- Secret/redaction impact: workspace names and stable IDs are stored and returned;
  request bodies are not logged. M2 has no credentials or arbitrary artifact text.
- External side effects: owner-only local database/WAL/SHM files only; no Git,
  network, subprocess runtime, provider, or human-message effect.
- Human approval boundary: the local operator explicitly starts the daemon and
  invokes the only mutation. M2 has no autonomous actor or policy delegation.

Database and lock files use mode `0600` inside a newly created `0700` data
directory. Event update/delete triggers enforce journal immutability, and foreign
keys reject events for absent workspaces. Crewfold refuses to adopt an unrelated
SQLite database rather than migrating it.

## Compatibility

- API/schema changes: additive protocol-v1 methods `database.status`,
  `workspace.init`, `workspace.show`, and `events.list`; new stable result/parameter
  URNs and domain workspace/event schemas.
- Storage evidence: one embedded current baseline and exact identity metadata;
  foreign schemas are refused.
- Adapter/runtime compatibility changes: none; no runtime or provider adapter is
  implemented.
- Earlier milestone scenarios rerun: M0 and M1 pass unchanged in every full gate.
- Restore impact: backups restore to a new data directory and must satisfy current
  identity and integrity checks before use.

## Known limitations and deferrals

- Workspace creation is the only durable domain mutation; workspace update/delete
  and all project, checkout, agent, task, message, knowledge, and runtime records
  remain absent.
- The placeholder actor is `local-owner`/`human`; run-scoped capabilities and
  policy authorization begin in later milestones.
- The database uses one connection, appropriate for M2 correctness but not yet
  personal-100 load evidence.
- Event listing is paginated polling; there is no live subscription or event
  retention/compaction policy.
- The foreground daemon still requires explicit data/socket paths and Unix-domain
  socket support.
- Backup/restore commands, repair/rebuild tooling, and packaged service operation
  remain deferred.
- Vendoring the portable SQLite driver adds about 14 MiB of third-party source and
  generated code; dependency updates remain explicit review events.
- Projects, repositories, checkouts, and read-only Git observation are deferred to
  M3.

## Repository hygiene

- Working tree clean after implementation acceptance: yes.
- No leaked processes/sockets/temp directories: yes.
- No paid/network call in default tests: yes.
- Documentation matches behavior: yes.
- Vendored dependency licenses retained: yes.
- No upstream Git remote created: yes.

## Decision

- Exit gate satisfied: `yes`.
- Waivers: none.
- Next milestone entry criteria met: `yes`.
- Next milestone: `M3 — Projects, repositories, and checkouts`.
