# ADR-0001: Local-first personal control plane

- Status: accepted
- Date: 2026-08-12

## Context

The long-term idea can expand into a company-wide coordination system, but the
immediate need is one developer managing their own agent sessions. Starting with
multi-user infrastructure would introduce identity, tenancy, distributed state,
and policy problems before validating the basic coordination model.

## Decision

Build the first Crewfold as a single-user local daemon with local storage and a
user-owned socket. It may coordinate many projects and agent roles but does not
model an organization or require a hosted service.

Keep entity IDs, protocols, and adapter boundaries suitable for a later remote
control plane. Do not make organization features a dependency of local operation.

## Consequences

- Installation and development stay small.
- Normal coordination works offline except for provider inference.
- SQLite and operating-system user permissions are sufficient initial primitives.
- Multi-user collaboration, cross-machine consistency, and enterprise policy are
  explicitly deferred.
- A future organization product extends the local node rather than sharing the
  exact same deployment architecture.
