# Knowledge system

## Purpose

Crewfold's knowledge system supplies the smallest trustworthy context needed for a
task. It is not a transcript archive disguised as memory.

The canonical knowledge core and context-packet v3 contract are implemented and
their acceptance/review gate has passed. The current implementation supports:

- canonical `decision` and `finding` items with immutable content revisions;
- task, concluded-meeting, and accepted meeting-proposal provenance;
- owner-governed acceptance, rejection, staleness, and supersession;
- proposals from either the owner CLI or an authenticated agent run;
- explicit, exact revision links in bounded context packets; and
- explanation of selection, exclusion, revision, and byte budgets;
- deterministic, scoped full-text discovery over canonical revisions; and
- an inspectable, explicitly rebuildable derived search index;
- a read-projected curator queue over proposed revisions; and
- one disabled-by-default, owner-configured deterministic rule that can copy an
  accepted structured meeting resolution into canonical project knowledge; and
- owner-confirmed exact-revision contradiction records with derived bounded
  dispute state and fail-closed retrieval/context behavior.

It does not ingest provider transcripts, run a model-backed curator or background
curation loop, semantically detect conflicts, automatically reconcile them, or
refresh a running agent with context deltas. Export/import also remains later.

The system separates four layers:

```text
raw observations -> evidence/artifacts -> proposed knowledge -> accepted knowledge
```

Each transition reduces noise and increases the governance required.

## Implemented canonical record

### Identity and lifecycle

A stable knowledge item has a `know_...` ID, workspace, project, optional task
applicability scope, and type. Its content lives in numbered `krev_...` revisions.
Revision title, body, quality metadata, provenance, freshness policy, and
supersession link are immutable after proposal. Governance changes advance a
separate state revision rather than rewriting content.

Review and currency are independent axes:

- review is `proposed`, `accepted`, or `rejected`;
- currency is `pending`, `current`, `stale`, or `superseded`.

A new proposal is pending. Accepting it makes it current. The owner may mark
current accepted knowledge stale. To correct accepted content, a caller proposes a
successor with `--supersedes`; accepting that successor atomically marks the prior
revision superseded while retaining both bodies and their audit history. Proposing
a successor alone does not displace the current revision.

### Scope and provenance

Every revision has one primary source and may have supporting sources, up to 16
total. Implemented source types are:

- a task at its recorded revision;
- a concluded meeting at its recorded revision; or
- an accepted meeting proposal whose meeting is concluded, at the proposal's
  recorded revision.

All sources must belong to one workspace and project. The primary source derives
the knowledge project; an explicit `--project` must match it. `--task-scope` may
narrow applicability to one task in that project. A task source does not by itself
make the knowledge task-only: without `--task-scope`, the revision is project-wide.

The record also carries confidence (`low|medium|high`), verification status
(`unverified|supported|verified`), and either `until_superseded` freshness or an
RFC3339 expiry. Sources are stored in deterministic order with frozen source
revisions, so the knowledge body is useful and inspectable without a transcript.

### Authority

The local owner may propose, accept, reject, or mark knowledge stale through the
CLI. An authenticated run may call `crewfold_propose_knowledge`; its primary source
is fixed to that run's task and its actor identity is fixed to the run. Tool
arguments cannot choose another actor or source project.

Agent proposals still require owner acceptance. There is no run-scoped accept,
reject, or stale tool: invoking a reserved governance name is rejected by the
immutable capability policy and produces `run.tool_denied` without touching the
revision. As defense in depth, any non-owner call that reaches the internal
knowledge-governance boundary preserves the revision and commits a `kauth_`
authority check plus its action-specific denial event. Owner decisions also
produce authority checks. Governance mutations require the state revision the
owner inspected.

## Context packet v3

The packet builder continues to snapshot the assigned role, exact task and
checkout revisions, direct dependencies, policy, reporting instructions, and a
bounded project inbox summary. Packet v3 adds explicit canonical knowledge; active
claim snapshots and full message bodies remain excluded.

`crewfold context build ... --include krev_...` accepts at most 16 unique revision
IDs. Requests remain in caller order in `requested_knowledge_revision_ids`. There
is no project-wide scan or implicit retrieval.

For each exact requested ID, the builder includes the complete revision snapshot
only when it is:

- accepted and current at build time;
- in the task's workspace and project;
- project-wide or explicitly scoped to that task; and
- still within an explicit freshness deadline, when one exists.

An unknown revision ID fails the build. Known but ineligible revisions remain out
of the packet with a stable reason: `proposed`, `rejected`, `stale`,
`superseded`, `out_of_scope`, or `over_budget`. A superseded exact pin is not
silently replaced; its exclusion may identify the current successor as
`replacement_revision_id`, and the caller must explicitly request that successor
to include it.

Packets have a fixed 32 KiB total limit and a 12 KiB accepted-knowledge sub-budget.
Knowledge is included whole or not at all; Crewfold never truncates a decision or
finding to make it fit. The packet and explanation report total and knowledge
limit, used, and remaining bytes. `context explain --output json` also exposes the
exact included and excluded entities and revisions.

Eligibility is evaluated once, inside the packet-build transaction. The resulting
snapshot is immutable: later acceptance, staleness, expiry, or supersession does
not rewrite an existing packet or invalidate its run binding. A new packet build
evaluates current state again. Context-packet schemas v1 and v2 remain readable;
new builds use v3.

Raw terminal and provider transcript text is not a packet input and is recorded as
an explicit exclusion. The replacement-agent path combines a durable handoff or
mailbox message with explicitly pinned accepted knowledge, not copied session
history.

## Deterministic retrieval and bounded curator

The first M15 slice implements deterministic SQLite FTS5 discovery over canonical
revision titles and bodies. `knowledge search` first applies hard workspace,
project, authority, currency, and freshness rules from canonical records. A broad
project search includes only project-wide records. Supplying a task adds records
scoped to that exact task and enables task/dependency provenance affinity; it does
not widen project scope.

Eligible matches use the versioned `knowledge_search_v1` lexicographic policy:
exact-task applicability, task/dependency provenance affinity, freshness horizon,
confidence, verification, weighted title/body BM25 score, acceptance time, then
exact revision ID. The complete tuple and reason for every result are returned
with the exact canonical revision, evaluation time, canonical cursor, and index
generation. Retrieval is a candidate-discovery query only: it cannot accept
content, follow a successor, inject results into a packet, or change canonical
state.

The FTS index is a disposable projection with a deterministic source digest. A
missing, corrupt, inconsistent, or out-of-date index produces an explicit
`retrieval_degraded` diagnosis instead of a fallback query or false empty result.
Exact `knowledge show`, `knowledge list`, and context-packet reads continue to use
canonical tables. `knowledge index rebuild` reconstructs the projection from those
tables; `knowledge index status` and `doctor --retrieval` expose its health. See
[ADR-0009](decisions/0009-deterministic-derived-knowledge-retrieval.md).
After a proposal commits, atomic best-effort catch-up may append its immutable row
and refresh the cursor/count/digest inside the current generation, but only when
the preexisting projection still matches its published snapshot. It never repairs
missing, corrupt, or content-mismatched derived state; only a full rebuild creates
a new generation and repairs damage.
Rebuild idempotency is tied to the published generation and source digest: an
unchanged healthy snapshot replays, degradation reports `retrieval_degraded`, and
a healthy superseding snapshot makes the old key conflict. Index/search event
cursors are node-wide journal observation high-water marks rather than retrieval
freshness or workspace-local knowledge cursors.

The implemented curator begins as an explicit deterministic pipeline rather than
an all-powerful memory agent. `curator queue` projects all currently proposed
canonical revisions in stable proposal order and classifies them for manual review
or the one exact safe rule. Each page includes the effective rule snapshot, so
its enabled state and revision are observable before an optimistic configuration
change. There is no independently mutable queue table: the proposal, its
governance state, and any immutable curator derivation remain the inspectable
records.

Every workspace stores `accepted_meeting_resolution_copy/v1` disabled at revision
one. An owner may enable or disable it with an optimistic expected revision.
`curator process` is an explicit local operation, not a background loop. Without
`--apply-safe`, it only derives; the flag is the explicit opt-in to evaluate safe
automatic acceptance. A pass scans at most 100 candidates and accepts at most ten.
Already-derived safe proposals are evaluated first. If capacity remains, exact
safe proposals newly derived from structured sources are revalidated and accepted
within the same opted-in transaction.
Processing while the rule is disabled may still derive a proposal from a
concluded meeting whose proposal was accepted; the result remains queued. After
the exact rule is enabled, a later opted-in pass may accept that same revision
through the distinct, revalidated curator governance path.

The transform is deliberately narrow: a `decision` whose title is the exact
meeting agenda and whose body is the exact accepted proposal summary; project-wide
scope; `medium` confidence; `supported` verification;
`until_superseded` freshness; and exactly one primary `meeting_proposal` source at
the accepted source revision. The agenda must be valid UTF-8 from 1 through 160
bytes and the summary valid UTF-8 from 1 through 2 KiB. Invalid source text is
skipped, never truncated; an accepted summary above 2 KiB reports
`summary_not_exact_safe_copy`. There is no task scope, supporting source,
predecessor, or caller-supplied transform field.

An auto-accept rechecks the enabled rule revision, source and output hashes,
derivation, source state, and proposed knowledge state in one transaction. Its
authority record is `subsystem:curator`, `allowed`, `state_policy`, and is linked
to immutable auto-acceptance evidence plus the normal `knowledge.accepted` event.
Calling the ordinary knowledge-governance path as a subsystem remains denied.
Agent proposals remain manual regardless of their confidence, verification label,
persuasive text, or claimed source. See
[ADR-0011](decisions/0011-bounded-deterministic-context-curator.md).

Later M15 slices add explicit refresh/deltas and portable export. Future
curator rules may cover structured handoffs, review findings, test evidence, and
explicit owner decisions, but each requires its own frozen transform and authority
contract; no general curator self-approval exists.

Long-running sessions do not currently receive knowledge changes. A future context
delta may report a dependency change, accepted or superseded decision, important
message, claim conflict, or explicit refresh request. Deltas will reference an
immutable base packet rather than mutate it.

## Planned broader knowledge types

M14 deliberately implements only decisions and findings. The broader product model
retains these planned types:

| Planned type | Example | Typical future authority |
| --- | --- | --- |
| Project brief | Purpose, users, current objective | Owner or manager |
| Constraint | “Server remains offline-capable” | Owner or accepted decision |
| Glossary | Domain meaning of “run” | Curator with review rule |
| Risk | Migration may invalidate old clients | Any agent proposes; owner triages |
| Runbook | How to execute a reliable operation | Maintainer or reviewer |
| Summary | Current state of a component | Curator; freshness-limited |

Future revisions may add component scope, labels, related entities, sensitivity,
sharing classification, contradiction records, and richer evidence/artifact
links. Those fields are not part of the implemented M14 authority contract.

Tasks, claims, messages, meetings, and live status remain coordination records and
are not duplicated as knowledge merely to make them retrievable.

## Where RAG fits

Retrieval-augmented generation is an implementation technique, not the knowledge
architecture. The implemented deterministic layer uses relational scope queries
plus SQLite FTS5; it performs no model call and does not make a search result
authoritative. Optional embeddings may later improve candidate discovery, but
they must be rebuildable from canonical records and can never become the sole
source of truth or acceptance.

A separate vector database is unjustified for the personal product until measured
recall or scale proves otherwise.

## Raw transcripts

Provider transcripts may remain in provider-native storage. Crewfold does not
ingest full transcripts into canonical knowledge or context packets by default
because they can contain secrets and unrelated repository content, amplify prompt
injection, consume large budgets, mix tentative reasoning with accepted outcomes,
and create provider-specific retention obligations.

If a future evidence policy retains an excerpt or reference, it remains evidence;
it does not become accepted knowledge without an explicit proposal and owner
decision.

## Management briefings are not shared memory

Canonical knowledge and management understanding serve different purposes.
Knowledge preserves reusable governed facts and decisions. A management briefing
is a current derived view across commitments, accepted outcome assessments,
decisions, evidence, checks, risks, overlaps, and follow-up work. It must not be
stored as a second, loosely governed version of project truth.

The planned briefing projection remains bounded and provenance-linked. Optional
model rendering may compress it for an audience but may not invent acceptance,
erase disagreement, or upgrade evidence strength. Raw transcripts are never
required to build it.

## Exact contradictions and export

Contradictions are a separate lifecycle over an immutable, lexically canonical
pair of exact revisions. Both participants must be different items in one project,
accepted/current, and have intersecting applicability. The pair is globally
unique: reversing it, dismissing it, or automatically resolving it does not make
the same immutable bodies reportable again.

An owner or live authenticated run may report. A run can report only when both
participants are project-wide or apply to its exact task; workspace, project,
task, and actor come from the run rather than tool arguments. Reports remain
`proposed` and do not affect retrieval. Only the owner can confirm an eligible
report `open`, and every confirmation/dismissal has optimistic revision and
durable authority/event evidence. The MCP confirmation name is reserved but not
advertised, so an agent probe is audited as `run.tool_denied` without creating a
governance attempt.

An open contradiction conservatively quarantines each whole exact participant
wherever it would otherwise apply. In particular, a project-wide claim paired
with a task-only claim is excluded from project search and other tasks too.
`knowledge dispute` reports total open incidence plus at most 200 sorted IDs.
Search removes disputed revisions before ranking/limit. A new packet build fails
atomically when an otherwise eligible explicit pin is disputed; older ordinary
ineligibility reasons take precedence, and previously built packet-v3 bytes never
change.

Owner dismissal removes the effect. Marking a participant stale or accepting its
successor atomically resolves all incident open records with exact governing
event/actor linkage; other open records can keep the revision disputed. See
[ADR-0012](decisions/0012-owner-confirmed-exact-knowledge-contradictions.md).

Automatic semantic detection and reconciliation are not implemented. The system
preserves both claims, provenance, scope, and resolution authority rather than
silently choosing the highest-ranked result.

Human-readable export plus machine-readable metadata remains planned. Provider
embeddings, hidden prompts, and proprietary databases must never be required to
recover canonical decisions and findings. Export must preserve exact contradiction
pairs, lifecycle/event links, and authority history without flattening dispute
into knowledge currency.
