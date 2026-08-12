# Knowledge system

## Purpose

Crewfold's knowledge system supplies the smallest trustworthy context needed for a
task. It is not a transcript archive disguised as memory.

The system separates four layers:

```text
raw observations -> evidence/artifacts -> proposed knowledge -> accepted knowledge
```

Each transition reduces noise and increases the amount of governance required.

## Knowledge types

| Type | Example | Typical authority |
| --- | --- | --- |
| Project brief | Purpose, users, current objective | Owner or manager |
| Constraint | “Server remains offline-capable” | Owner or accepted decision |
| Decision | Selected storage or API boundary | Named decision owner |
| Glossary | Domain meaning of “run” | Curator with review rule |
| Finding | Reproduced behavior or measured result | Agent plus evidence |
| Risk | Migration may invalidate old clients | Any agent proposes; owner triages |
| Runbook | How to execute a reliable operation | Maintainer/reviewer |
| Summary | Current state of a component | Curator; freshness-limited |

Tasks, claims, messages, and live status remain coordination records and are not
duplicated as knowledge merely to make them retrievable.

## Knowledge record

Every revision includes:

- type, title, and concise body;
- workspace/project/component/task scope;
- author and approving authority;
- source artifacts and events;
- confidence and verification status;
- validity interval or freshness policy;
- labels and related entities;
- the revision it supersedes or contradicts;
- sensitivity and sharing classification.

Revision bodies should be useful without reading the source transcript. Evidence
links let a reviewer drill down when necessary.

## The context curator

The curator is a role plus a deterministic pipeline, not an all-powerful memory
agent.

### Inputs

- structured handoffs and completion reports;
- accepted meeting resolutions;
- review findings and test evidence;
- explicit decisions and owner messages;
- stale or contradictory knowledge alerts.

### Responsibilities

- propose new or revised knowledge;
- merge redundant descriptions without losing provenance;
- mark stale content and identify contradictions;
- keep project and component summaries within size budgets;
- ensure accepted decisions appear in relevant future packets;
- avoid promoting speculation or transient status into shared truth.

### Acceptance policy

Low-risk normalization, such as adding a source link or fixing formatting, may be
automatic. Factual findings require evidence. Constraints and architecture
decisions require their named authority. Sensitive content may be excluded from
model-based curation entirely.

The curator cannot silently rewrite history. Accepted revisions remain available,
and supersession is explicit.

## Context packet assembly

A context packet is assembled at run start and can be refreshed at explicit
checkpoints.

### Required sections

1. Agent role, permissions, and escalation route.
2. Task outcome, deliverables, acceptance checks, and budgets.
3. Dependency state and relevant handoffs.
4. Active claims and known overlaps.
5. Applicable accepted constraints and decisions.
6. Relevant current findings, glossary, and runbook excerpts.
7. Unread or high-priority mailbox items.
8. Repository/checkout identity and current Git snapshot.
9. How to report progress, completion, blockage, and knowledge proposals.

### Selection order

Crewfold retrieves by hard scope and authority first:

1. explicit task links;
2. active project/component constraints and decisions;
3. direct dependencies and handoffs;
4. recipient messages;
5. deterministic full-text and label matches;
6. optional semantic matches below a bounded share of the packet.

Items are ranked by applicability, authority, freshness, confidence, and estimated
utility. Recency alone is not truth, and semantic similarity alone is not
relevance.

### Budgets

Every packet has section budgets and a total approximate token/byte budget. If the
packet is too large, Crewfold excludes lower-ranked items and records the reason.
It does not truncate a decision in a way that changes its meaning.

### Refresh

Long-running sessions do not receive every event. Crewfold sends a context delta
when:

- a dependency completes or changes;
- an applicable decision is accepted or superseded;
- an important direct message arrives;
- a claim conflict affects the task;
- the agent requests refresh;
- a configured checkpoint is reached.

The delta references the base packet and records what changed.

## Where RAG fits

Retrieval-augmented generation is an implementation technique, not the knowledge
architecture.

The MVP uses:

- structured relational queries;
- scope and label filtering;
- SQLite FTS5 full-text search;
- explicit graph traversal across tasks, decisions, artifacts, and dependencies;
- deterministic ranking with provenance.

Optional embeddings can later improve discovery across old findings, docs, and
semantically related tasks. They should retrieve candidates, not determine
authority or overwrite accepted knowledge. A pluggable embedding index can be
rebuilt from canonical records and therefore is never the sole source of truth.

A separate vector database is unjustified for the personal MVP. SQLite metadata,
FTS, and an optional local vector extension are enough until measured recall or
scale proves otherwise.

## Raw transcripts

Provider transcripts may remain in provider-native storage. Crewfold can record a
reference and a short retained excerpt when needed for evidence, subject to
redaction and retention policy.

Crewfold should not ingest full transcripts by default because they:

- contain secrets and unrelated repository content;
- amplify prompt-injection risk;
- are expensive to summarize repeatedly;
- mix tentative reasoning with accepted outcomes;
- create provider lock-in and retention obligations.

## Management briefings are not shared memory

Canonical knowledge and management understanding serve different purposes.
Knowledge items preserve reusable facts, constraints, decisions, findings, and
runbooks. A management briefing is a current derived view across commitments,
accepted outcome assessments, decisions, evidence, checks, risks, overlaps, and
follow-up work. It must not be stored as a second, loosely governed version of
project truth.

The structured briefing projection answers:

1. What was promised and what was accepted as delivered?
2. What materially changed since the selected owner checkpoint?
3. Which decisions shaped the result, and what constraints drove them?
4. Which verification supports reliability and stability, how independent is it,
   and is it still fresh?
5. What failed, deviated, conflicts, remains unknown, or still carries risk?
6. Which decisions or interventions now require the owner?

The base projection is bounded, revisioned, and provenance-linked. Optional model
rendering may compress it for a particular audience, but it receives the
structured projection as input and may not invent acceptance, erase disagreement,
or upgrade evidence strength. Raw transcripts are never required to answer these
questions; they remain optional drill-down sources when retained.

## Contradictions and staleness

Knowledge can be `current`, `stale`, `disputed`, `superseded`, or `withdrawn`.

When two accepted-looking items conflict, Crewfold does not blend them into a
confident summary. It creates a contradiction record, excludes unsafe derived
claims, alerts the responsible authority, and preserves both sources until a new
revision resolves them.

Freshness policies can be time-based or event-based. For example, an architectural
decision remains current until superseded, while a build-status summary becomes
stale when HEAD changes.

## Export and portability

Canonical knowledge must be exportable as readable Markdown plus machine-readable
metadata. Provider embeddings, hidden prompts, and proprietary databases cannot be
required to recover project decisions and briefs.
