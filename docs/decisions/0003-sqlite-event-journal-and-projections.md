# ADR-0003: SQLite event journal and projections

- Status: accepted
- Date: 2026-08-12

## Context

Crewfold needs current-state queries, durable work queues, auditability, crash
recovery, and a history of coordination decisions. A pure append-only store makes
ordinary product queries cumbersome; mutable tables alone make reconstruction and
audit difficult. A distributed database is unnecessary for one local owner.

## Decision

Use SQLite in WAL mode. In each mutation transaction, append an immutable domain
event and update normalized current-state projections. Keep handler logic explicit
and projections rebuildable where practical, without adopting an external
event-sourcing framework.

Use the database for queues, leases, idempotency, and full-text indexes. Store large
retained artifacts outside the database by content hash.

## Consequences

- Current queries and durable transactions remain simple.
- Every accepted mutation has a compact audit record.
- Schema migrations and event compatibility require disciplined testing.
- Background workers must keep transactions short and handle SQLite contention.
- Organization mode will need a different authoritative store while preserving
  event semantics and entity IDs.
