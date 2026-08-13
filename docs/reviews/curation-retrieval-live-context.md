# Milestone review — curation, retrieval, collaboration, and live context

## Identity

- Milestone: `M15 — Curator, deterministic retrieval, and context deltas`
- Review status: `passed`
- Final implementation commit: `c975ba4b17856b25c16dc5976d248e09a865d178`
- Reviewer: `automated acceptance, independent adversarial review, and full repository gate`
- Date: `2026-08-13`

## Demonstrable outcome

Crewfold can now find and maintain relevant canonical knowledge without making
retrieval the source of truth. Agents in adjacent registered projects can exchange
one-recipient mail through an owner-created participant thread, even while the
recipient is offline. A bounded curator can derive an exact meeting-resolution
copy, the owner can govern exact-revision contradictions, and project knowledge
can be exported and imported without restoring operational tasks, meetings, runs,
or provider state.

The current packet carries an immutable journal cursor, bounded reverse dependents, exact
participant rosters, and a frozen live-context policy. The owner explicitly scans
for changes; the exact live run fetches and acknowledges at most one pending
immutable delta. Unsafe drift, an unsupported event, or a byte/event limit never
truncates authority: it produces a durable, explainable rebase requirement.

## Public capability

- `knowledge search`, `knowledge explain`, retrieval status/rebuild, and retrieval
  diagnostics use deterministic, scope-filtered FTS5 ranking over a rebuildable
  projection.
- `thread create`, `thread invite`, and participant inspection bind every
  collaborator to one immutable agent/task/project identity while direct mail
  remains project-isolated.
- `curator queue`, rule configuration, and bounded processing derive only the
  frozen exact-copy rule and retain owner governance.
- Exact contradiction report/confirm/dismiss/list/show/dispute surfaces preserve
  participant revision identity and exclude open disputes before retrieval limits.
- `knowledge export` and `knowledge import` use canonical two-file project bundles,
  exact digests, owner-only empty-scope import, non-operational task anchors, and
  local import attestations rather than replayed origin authority.
- `context refresh` plus delta list/show/explain gives the owner a bounded pull
  surface. `crewfold_get_context_delta` is fetch-only and
  `crewfold_acknowledge_context_delta` is restricted to the exact live bound run.

## Authority and integrity proof

- Retrieval, curator projection, export rendering, and FTS never grant acceptance
  or task authority; canonical SQLite rows remain decisive.
- Cross-project delivery requires an exact active participant binding. A run for a
  different task of the same agent cannot receive, read, acknowledge, or wake for
  that mail.
- Context deltas contain only closed typed changes. Whole changes either fit the
  16 KiB delta and 64 KiB chain limits or force rebase; no partial authority is
  delivered.
- A delta acknowledgement records consumption only. It does not read mailbox
  messages, govern knowledge, resolve contradictions, or mutate work state.
- Stored delta hashes are checksums, not authenticity by themselves. Every chain
  read revalidates message previews, knowledge, contradiction history, dependency
  snapshots, participant rosters, cause events, and exact scope against canonical
  projections and historical journal facts. Self-hashed forged payloads and orphan
  rows fail closed across fetch, list, show, explain, refresh, acknowledgement, and
  restart.
- Packet and delta state, events, idempotency receipts, acknowledgements, and
  rebase projection changes commit atomically. Failure injection after projection
  and event boundaries leaves no partial mutation and retry succeeds.

## Bounds and current contract

- The current packet remains within 32 KiB, with 12 KiB knowledge and 8 KiB collaboration
  sub-budgets, at most 32 reverse dependents, and at most eight complete rosters.
- One refresh inspects at most 1,000 potentially relevant events. Known inert
  lifecycle noise is pruned before the bound; unknown authority-sensitive facts
  fail closed.
- Freshness is evaluated with exact RFC3339 nanosecond ordering. Expiry can create
  a withdrawal at an unchanged event cursor without inventing a journal cause.
- A packet's frozen policy never gains ungranted MCP tools; a changed base contract
  yields an explicit rebase requirement and creates no delta state.
- Provider execution stays explicit-pull. M15 adds no native provider steering,
  model call, network dependency, or transcript ingestion.

## Acceptance evidence

The final stable tree passed:

- generated database source/output consistency;
- formatting and `go vet ./...`;
- `go test ./...` and `go test -race ./...`;
- every M0–M15 black-box scenario, including deterministic retrieval, bounded
  curator, cross-project collaboration, exact contradictions, portable knowledge,
  and live context deltas;
- provider-neutral Herdr, recorded Codex, and recorded Claude/provider-handoff
  fixtures without model or network use.

The live-context scenario proves pending delivery, daemon restart, exact-run
fetch/ack and idempotent replay, wrong-task and wrong-run denial, preview bounds,
dispute suppression tombstones and final-close re-offer, no-op cursor advancement,
pagination, current-packet frozen-tool enforcement, trigger integrity, and exact
event counts.
Independent adversarial review and a separate final-gate audit found no unresolved
high- or medium-severity defect.

## Known deferrals

M15 does not resume or steer a provider turn, merge context into an already-read
prompt, create organization-wide authority, enable embeddings, or acknowledge
source projections on an agent's behalf. Manager proposals, supervisor scheduling,
approval queues, and automated policy actions begin in M16.
