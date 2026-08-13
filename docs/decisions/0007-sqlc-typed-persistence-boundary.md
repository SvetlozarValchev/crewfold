# ADR-0007: sqlc typed persistence boundary

- Status: accepted
- Date: 2026-08-12

## Context

Crewfold deliberately keeps SQLite transactions and schema behavior explicit.
As the schema grew, direct `database/sql` calls also produced positional row
scanners, anonymous parameter lists, and duplicated database/domain conversion.
Those call sites compile even when a query's selected columns and its scanner are
accidentally reordered, which makes a coordination control plane unnecessarily
hard to review.

Prisma-style generated access is attractive, but Prisma targets a JavaScript or
TypeScript runtime and would add another application runtime to the Go daemon.
Full Go ORMs also obscure some of the short, multi-projection transactions,
partial indexes, recursive dependency checks, and event-journal writes Crewfold
needs to audit directly.

## Decision

Use pinned `sqlc` to generate the Go persistence boundary from named SQLite query
files. Keep ordered SQL migrations as the authoritative schema. Generated code is
checked in; handwritten domain services construct typed query parameters and own
transaction boundaries, policy validation, event ordering, and conversion into
domain records.

New persistence work uses generated queries by default. Existing direct SQL is
migrated incrementally when its subsystem is changed. Exceptional dynamic or
transaction-specific SQL may remain handwritten only when a generated query is
not a clearer representation, and it requires focused tests.

The repository pins the generator version in `.sqlc-version`. Normal builds and
the complete offline gate do not download or execute the generator: a deterministic
hash detects changes to migration/query sources without refreshed checked-in
output. Contributors run `./scripts/generate-db.sh` after changing those sources.

The generator reads an ordered temporary projection of the migrations with
`CREATE TRIGGER` bodies removed. Triggers are runtime integrity programs rather
than query result schemas, and sufficiently large SQLite trigger expressions can
make the pinned parser pathologically slow. The authoritative migration files,
including every trigger byte, remain part of the source hash and are executed and
tested unchanged by Crewfold; only sqlc's development-time schema input omits
them.

### Schema-17 supervision exception

Schema 17's management/supervision service is an explicit transaction-specific
exception to the default. Its writes are not independent CRUD operations: one
proposal decision or placement interleaves optimistic revision checks, normalized
authority children, graph and capacity reads, projection writes, immutable
journal facts, integrity receipts, fault barriers, and an idempotency receipt in
one `BEGIN IMMEDIATE` transaction. Splitting that sequence between generated and
handwritten query objects made the security ordering harder—not easier—to audit,
so `internal/store/management.go` keeps the complete fixed SQL program together
for M16. It does not construct SQL from caller data; only values are parameters.

This exception is bounded to schema-17 tables and their atomic orchestration. It
is covered by strict row scanners and canonical-hash checks on every read,
direct-SQL substitution/partial-graph tests, transaction fault barriers,
concurrent supervisor and proposal tests, restart tests, the generated schema
object-manifest check, and the provider-free manager/supervisor scenario. New
unrelated persistence still uses named sqlc queries by default. A later
mechanical conversion may move whole fixed read/write units to named queries,
but must preserve transaction/event/receipt ordering and the same executable
security matrix; it is not a license for a second dynamic persistence layer.

## Consequences

- Query inputs, nullable fields, results, and row scanning are compile-time Go
  types.
- SQL and transaction behavior remain visible and reviewable.
- Generated code adds repository volume but removes handwritten scan boilerplate.
- Schema/query changes require the pinned generator before committing.
- The codebase temporarily contains older direct SQL alongside generated access;
  this is an explicit migration state, not a second preferred pattern.
