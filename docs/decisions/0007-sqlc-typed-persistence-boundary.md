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

## Consequences

- Query inputs, nullable fields, results, and row scanning are compile-time Go
  types.
- SQL and transaction behavior remain visible and reviewable.
- Generated code adds repository volume but removes handwritten scan boilerplate.
- Schema/query changes require the pinned generator before committing.
- The codebase temporarily contains older direct SQL alongside generated access;
  this is an explicit migration state, not a second preferred pattern.
