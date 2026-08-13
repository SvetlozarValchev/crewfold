# ADR-0009: Deterministic derived knowledge retrieval

- Status: accepted
- Date: 2026-08-13

## Context

Exact knowledge revision links make context packets reproducible, but an operator
cannot know every relevant `krev_...` identifier once a project has many accepted
decisions and findings. Full-text discovery is useful only if it cannot bypass the
scope, authority, freshness, and exact-link guarantees established for canonical
knowledge. A search index is also a fallible derived projection: treating missing,
stale, or corrupt index state as an empty result would silently hide knowledge.

The first retrieval slice needs one ordering that remains stable across retries and
restart, a result that explains every ranking input, and a repair path that never
rewrites canonical revisions. Embeddings and model judgment are deliberately absent
from this boundary.

## Decision

Crewfold uses a disposable SQLite FTS5 projection over the immutable title and body
of every canonical knowledge revision. FTS discovers text matches; canonical tables
remain the source for the returned revision and for every eligibility decision.
Search is read-only. It cannot accept a proposal, mutate a revision, build a context
packet, or silently substitute a successor.

A query is normalized as one to 16 whitespace-separated literal terms rather than
accepted as FTS syntax. Its trimmed UTF-8 representation is at most 256 bytes.
The public normalized query joins those terms with one ASCII space; a separate
quoted `AND` expression is compiled only for FTS. The index uses the SQLite
`unicode61` tokenizer with diacritic removal. Title
matches have weight `8.0`; body matches have weight `1.0`. The public limit
defaults to 20 and cannot exceed 100.

Eligibility is evaluated at one captured instant and canonical event cursor. A
result must be in the requested workspace and project, accepted, current, and not
past an explicit freshness deadline. An optional type is a hard filter. Search with
a task returns project-wide revisions plus revisions scoped to that exact task.
Search without a task returns project-wide revisions only; it never exposes a
task-local record merely because its text matches. Proposed, rejected, stale,
superseded, expired, and out-of-scope revisions are omitted rather than presented
as candidates. Provenance contributes affinity but never authority.

Eligible matches use this lexicographic order, with a final exact revision-ID tie
breaker:

1. applicability: exact task before project-wide when a task is supplied;
2. provenance affinity: primary exact-task source, supporting exact-task source,
   primary direct-dependency source, supporting direct-dependency source, then no
   task affinity;
3. freshness policy: `until_superseded` before an explicit deadline, then later
   deadlines before earlier deadlines;
4. confidence: `high`, `medium`, then `low`;
5. verification: `verified`, `supported`, then `unverified`;
6. raw weighted FTS5 `bm25` score ascending, without rounding or sign changes;
7. acceptance time descending; and
8. knowledge revision ID ascending.

Project-only search reports neutral applicability and provenance ranks because
task affinity was not requested. The ranking policy is named
`knowledge_search_v1`. Results include the complete exact canonical revision,
ordinal, raw score, each tuple component and reason, evaluation time, canonical
cursor, and the search-index generation/digest that produced the match. A later
consumer may freeze those exact candidate facts; it must not reconstruct them by
rerunning search.

The index publishes generation metadata only after a complete transactional
rebuild from canonical records and an integrity/content-consistency check. Its
source digest is deterministic over revision IDs and canonical content hashes in
revision-ID order. Missing, corrupt, inconsistent, or out-of-date index state
returns the stable `retrieval_degraded` error. Crewfold does not fall back to
`LIKE`, return a misleading empty success, or repair as a side effect of search.
Exact knowledge reads and exact context packets remain available while retrieval
is degraded. Index status and `doctor --retrieval` expose the diagnosis; an
explicit idempotent rebuild repairs it without changing canonical revisions,
authority records, context packets, or the event journal.

After a canonical proposal commits, a best-effort transaction may incrementally
catch a previously healthy projection up by inserting immutable revision rows and
updating `built_at`, the global journal observation cursor, source count, and
source digest together. It first proves that the existing rows still exactly
match the previously published count and digest. This catch-up retains the same
generation; only a full rebuild creates a generation. It does not recreate a
missing index or repair corrupt or content-mismatched rows. Consequently,
generation plus source digest identifies the current derived content, and an
operator still uses an explicit rebuild to repair damage.

Rebuild replay is scoped to the exact derived snapshot it published. An immediate
retry with the same request and idempotency key replays only while the current
index is healthy and its generation and source digest still match that recorded
result. A degraded current index returns `retrieval_degraded`; a later healthy
rebuild or canonical refresh that supersedes the recorded snapshot returns
`idempotency_conflict`, and the operator repairs or rebuilds with a new key. This
prevents an old success response from describing a different current projection.

`source_event_sequence` and `canonical_event_sequence` are transactionally
captured high-water marks from the node-wide event journal. They identify when a
projection or search observed that global journal; they are diagnostic provenance,
not retrieval-freshness checks and not workspace-scoped knowledge-event cursors.

## Consequences

- Search results are explainable suggestions, never a new form of knowledge
  authority.
- Task-scoped content cannot leak into a broad project query.
- Identical canonical state, query, task, clock, and healthy generation produce
  the same exact revision order.
- Operators see retrieval failure instead of silently losing candidate recall.
- The FTS projection can be removed and reconstructed without knowledge loss.
- Exact context packets retain their explicit-pin behavior; search output is not
  implicitly injected into a run.
- Ranking-policy changes require a new named policy and contract rather than an
  unnoticed weight change.
- Curator policy, contradictions, context deltas, export/import, measured utility,
  embeddings, and broader knowledge types remain separate M15 work.
