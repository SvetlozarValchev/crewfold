# Domain model

## Identity rules

Every durable entity has:

- an opaque globally unique ID;
- a human-readable name unique only within its declared scope;
- a monotonically increasing revision;
- creation and update timestamps;
- creator and last-mutating actor IDs;
- optional labels and annotations.

Names can change. Paths can move. Provider session IDs can disappear. Durable
Crewfold IDs must continue to work.

## Ownership hierarchy

The personal product uses a deliberately small hierarchy:

```text
workspace
├─ projects
│  ├─ repositories
│  ├─ checkouts
│  ├─ knowledge scopes
│  └─ tasks
├─ teams
│  └─ agent definitions
└─ policies and budgets
```

A workspace belongs to the one local owner. “Team” is an organizational grouping,
not a security tenant. Organization and user entities are deferred.

## Core entities

### Workspace

The top-level local coordination scope. It owns configuration defaults, projects,
teams, agents, and policies. A personal installation may have several workspaces,
but only one is needed initially.

### Project

A coordinated body of work with an objective, policies, knowledge, tasks, and one
or more repositories. A project is not synonymous with one Git repository.

### Repository

A stable identity for one Git history. Multiple local checkouts or worktrees may
point to it. Remote URLs are attributes and cannot serve as the only identity
because remotes can change or be absent.

Crewfold derives a local history fingerprint from the repository object format and its
sorted reachable root commits. This lets independently cloned adjacent directories
share a repository record without depending on a remote URL or common Git
directory. The fingerprint is an observation-based identity, not a Git-provided
global UUID; a later reconciliation command handles histories that are rewritten
or found to have been grouped incorrectly.

### Checkout

A concrete filesystem location and branch/HEAD state on one node. A checkout has a
write policy:

- `exclusive`: one writable task/run at a time;
- `claimed`: concurrent writers allowed only with non-conflicting claims;
- `shared`: concurrent writing allowed with warnings;
- `read_only`: no Crewfold-launched mutation.

`exclusive` or separate Git worktrees are the default for implementation tasks.
The term does not imply `git worktree`: a standalone clone, a copied repository,
and a linked worktree are all checkouts when they occupy distinct concrete paths.

### Team

A named grouping of agent definitions with an optional manager and shared project
scope. Teams help humans reason about a large crew; they do not imply a permanent
hierarchy or provider choice.

### Agent definition

A durable role identity. It contains:

- name, role, and operating instructions;
- eligible projects and task classes;
- provider and runtime preferences;
- capabilities and required tools;
- action policy and budgets;
- concurrency limit, normally one;
- optional manager/team relationship.

An agent definition never means a process is currently running.

The implemented subset stores name, role, provider/runtime preference, enabled
state, maximum concurrency, revision, and audit metadata. Instructions,
capabilities, team membership, and action policy arrive with the capabilities that
consume them. M16 launch profiles provide exact project/agent scheduling bindings.
Provider/runtime values remain opaque data.

### Run

One concrete attempt to execute work using an agent definition. The implemented
run binds:

- an agent definition and provider adapter;
- a runtime driver and runtime handle;
- one primary assigned task;
- one eligible checkout, whether it is a standalone clone or linked worktree;
- one immutable context packet fixing role/task/checkout revisions and policy;
- opaque runtime/provider names and handles;
- an explainable placement decision;
- a persisted fake-scenario cursor, timestamps, result, and failure diagnosis;
- normalized progress, blockage, completion evidence, and an accepted handoff.

Run state:

```text
requested -> starting -> active -> completed
                         |   |----> blocked -> active
                         |   |----> stopping -> stopped
                         |   |----> lost
                         |   |----> review
                         |   \----> failed
                         \--------> start_failed
```

`blocked` and an explicit active checkpoint are resumable. `stopped` means an
operator stop completed and records whether forced termination was required;
the task remains assigned for an explicit retry/reassignment decision. `lost`
means process identity or outcome cannot be trusted, so the task is blocked and
capacity remains reserved. `review` means the
provider proposed completion but required acceptance evidence was missing; the
task becomes `changes_requested` and retains its assignment. Runtime-observed
state and Crewfold run state are related but not identical. The direct fixture
runtime now persists bindings, bounded output, timeout, stop, and restart
reconciliation. A run-scoped capability exposes its packet and normalized
reporting tools without granting task-completion authority. Real provider resume
handles, heartbeats, usage accounting, and enforced budgets remain planned.

### Context packet

An immutable, bounded base briefing built for one exact task/agent/checkout and
bindable to at most one run. It may exist unbound after an explicit build; a
successful `run.start` creates the unique binding. It snapshots the assigned agent
role, exact task revision, selected checkout and repository observation, direct
dependency revisions, bounded same-project reverse dependents, allowed tools,
denied/approval-required operations, reporting instructions, whole authorized
participant-thread rosters, and a bounded authority-scoped summary of unseen inbox
items. This normally means project scope and additionally permits an exact
participant agent/task/project binding. Full message bodies remain explicit
mailbox reads; active claim
snapshots and provider transcripts are deliberate exclusions.

Ordinary new builds use context-packet schema v4. The caller may provide up to 16 ordered,
unique knowledge revision IDs. The packet preserves those exact requests and
includes complete snapshots only for revisions that are accepted, current, fresh,
and applicable to the task's project and optional task scope. It never searches
for related knowledge or silently follows a superseded pin. Proposed, rejected,
stale, superseded, out-of-scope, and over-budget requests are explained per
revision; a superseded exclusion may identify the current replacement as metadata.

The total packet limit is 32 KiB, including a 12 KiB knowledge sub-budget and an
8 KiB participant-roster sub-budget. Direct dependencies are complete and capped
at 32; reverse dependents are informational, sorted, and capped at 32; authorized
participant threads are selected as whole snapshots, newest update then ID, and
capped at eight. An item is included whole or excluded. The packet freezes its
source event high-water and live policy. Existing v1, v2, and v3 packets remain
readable and immutable after an upgrade but cannot acquire live tools.

The packet's semantic content hash excludes packet identity and creation metadata,
so equivalent controlled inputs—including ordered explicit knowledge links—have
the same hash while retaining distinct packet IDs. Eligibility is evaluated when
the packet is built, and the bytes never change. An explicitly prebuilt v4 packet
is revalidated once when a run binds it: frozen run authority must still match,
and embedded knowledge must still be accepted, current, fresh, applicable, and
undisputed. A failed binding requires a new packet. After successful binding,
later changes do not rewrite or silently refresh the base; explicit deltas carry
withdrawals and durable rebase reports an unsafe base contract. Historical v1–v3
packets remain readable but cannot acquire live delivery authority.

An owner-authorized management-capability build uses schema v5 instead. It
preserves the complete v4 contract and adds one exact, immutable manager-grant
snapshot. Ordinary runs and historical packets never acquire manager proposal
tools merely because their owner-defined descriptive role labels happen to match.

### Launch profile

A launch profile is an owner-created, revisioned execution recipe. One exact
revision fixes a project and exact agent revision, runtime/provider selection,
validated fixture or provider scenario, acceptance contract, process limits, and
write mode. Its purpose and any task-role match are workflow metadata, not
authority. A profile is
the only source from which the supervisor may construct a new run; model text is
never interpreted as an executable, argument vector, environment, provider, or
capability set.

The profile itself is the scheduling-eligibility record: it names one exact
project, agent revision, and checkout/runtime/provider recipe. There is no hidden
project-agent eligibility table or role-derived rank. Team/workspace membership
and `AgentDefinition.Role` alone do not make an agent schedulable. Retiring a
profile prevents a new placement without rewriting an already committed run intent.

### Manager grant and proposal

A manager grant is a bounded owner delegation to one exact agent, assigned task,
project, and objective. Its revision freezes expiry, allowed proposal kinds,
exact target profile revisions, and task, action, token, cost, and time ceilings.
The grantee's role remains arbitrary descriptive metadata. The current grant is
revalidated both when packet v5 binds and on every proposal call; revocation leaves
historical packet bytes intact but denies later calls.

A manager proposal is immutable, typed, and inert. It contains at most 32 ordered
actions and 48 KiB (49,152 encoded bytes) of canonical content covering task creation, same-project
dependencies, claim requirements, assignment nominations, review requirements,
and owner escalation. Submission creates proposal/audit state only. In particular,
it creates no task, dependency, assignment, claim, context packet, or run.

Only the local owner accepts or rejects a proposal. Acceptance revalidates the
whole proposed graph, exact source revisions, scope, grant/profile binding,
claims, and finite budgets in one immediate transaction, then applies every action
or none. A proposed claim is only a later scheduling requirement, a nomination is
only a preference, and a review requirement grants no completion authority.

### Scheduling intent

An accepted assignment-producing action creates one durable scheduling intent
for an exact task, agent, and launch profile. The open states are `pending`,
`deferred`, `awaiting_approval`, and `run_requested`; at most one may exist per
task, and manual assignment cannot replace it. A committed placement retains the
original intent across bounded fresh-run retries. Completion produces
`satisfied`, rejected completion or definitive failure produces `failed`, and a
stopped run produces `cancelled`. A definite start failure remains open only
while current policy can still authorize another exact retry. Owner task
cancellation can close pending/deferred work, or retry-pending work only when its
exact latest receipt-linked run is definitively `start_failed`.

### Supervisor policy, condition, action, and approval

An owner-revisioned supervisor policy names a closed set of automatic actions and
global, project, provider, and agent concurrency limits. The initial automatic
action is `schedule_ready`; an optional exact policy may also permit a same-profile
`start_failed` retry with an exact zero-through-three limit and cooldown. Each
retry preserves the failed run and creates a fresh run; its action distinguishes
`prior_run_id` from the new `run_id`. All other conditions, including wall-time
over-budget and `manager_escalation`, produce an inert approval request.

A supervisor condition freezes the triggering facts and their revisions. A
supervisor action freezes the condition, policy, placement proof, capacity counts,
stable reason codes, and optional run IDs. A manager escalation additionally
freezes the exact accepted `source_proposal_id`/`source_action_id` pair. One condition/action origin and one
scheduling receipt make repeated scans idempotent. An approval is a single owner
decision over one still-current request; it cannot authorize a different target
after the evidence becomes stale.

### Context delta and acknowledgement

A `cdelta_...` context delta is an immutable, bounded change object for one exact
live run and one packet-v4 base. It records workspace/project/task/agent scope,
base packet identity and schema, a run-local sequence and optional parent, an
exclusive source event cursor and inclusive inspected cursor, evaluation time,
typed whole changes, inclusion/exclusion explanations, total/chain budget
accounting, content hash, size, and creation audit fields.

Delta changes form a closed union: bounded inbox preview, newly accepted decision
or eligible decision re-offer, known-knowledge withdrawal or no-body disputed
suppression tombstone, contradiction opened/closed, reverse dependent
addition/update, or complete participant-roster update. A suppression tombstone
records a post-base accepted applicable decision hidden by an open contradiction;
it does not disclose the decision body. Direct-upstream set or snapshot drift is
an authority-contract change and requires rebase rather than a delta.
Event payloads provide causes; current canonical projections provide the delivered
snapshot. A time-driven expiry withdrawal can have equal source cursors.

One run-context state projection records the base cursor, last inspected cursor,
next/pending/acknowledged sequence, cumulative bytes, and
`ready|pending_ack|rebase_required` status. At most one delta may be pending.
Owner refresh builds or returns it; only the exact authenticated live run may add
one immutable `cdack_...` acknowledgement. Owner inspection is never consumption.
No-op refresh advances inspected state without creating a delta or event.

### Objective

A human-level desired outcome that can contain many tasks. An objective records
success criteria, priority, budget, policy, and status. Managers may propose a task
decomposition, but the objective remains the owner's statement of intent.

The implemented subset stores project scope, title, `active|completed|cancelled`
status, and token/cost/time budget. Success criteria and policy are planned.

### Task

A schedulable unit of work with:

- title and desired outcome;
- deliverables and acceptance checks;
- project and optional component scope;
- dependencies and parent task;
- expected change surface;
- assigned agent and assignment lease;
- priority, budgets, and action policy;
- evidence, handoffs, and result.

Task state:

```text
ready -> assigned -> active -> review -> completed
                    |          |          |
                    +-------> blocked <---+
                               \-> changes_requested
                    +-------> cancelled
                    +-------> failed
```

A run ending does not automatically complete its task. Completion is a domain
decision backed by evidence and any required review.

The implemented coordination subset starts tasks at `ready` and supports
`ready -> assigned -> active`, blocking from ready/assigned/active, unblocking to
assigned or ready, and cancellation. Dependency completion and review/completion
are introduced with the run loop. Assignments are separate durable records with
`active|expired|released` status; expiry never deletes history.

### Claim

A time-bounded declaration that an agent/task intends to own or modify a resource.
Claim subjects include:

- repository paths or globs;
- symbols when an index is available;
- components and APIs;
- test suites, migrations, schemas, or release operations;
- conceptual behaviors described by labels.

The implemented claim kinds are `path`, `component`, and `operation`; modes are
`exclusive`, `shared`, and `advisory`. Every claim belongs to one task/project,
has an expiry lease and conflict policy, and retains revision/audit history. Path
claims are also bound to one concrete checkout for drift attribution. Their
bounded repository-relative glob language supports literals, `*`, `?`, and
whole-segment `**`.

Observed Git changes are compared with the union of one task's active path claims
for that checkout. A dirty path that existed in the baseline snapshot is not
attributed to the new claim. A later out-of-scope path creates durable drift
evidence; it never retroactively expands authorization or rewrites the declaration.
Renewal and rationale fields remain future additions.

### Message

A durable envelope from one actor to a recipient. It has a message kind, thread,
optional project/task/artifact/reply links, and separate
delivery/read/acknowledgement and wake state.

Messages contain concise coordination content. Large evidence lives in artifacts
or source systems and is linked.

The implemented contract accepts one enabled agent recipient and a body of at most
4096 UTF-8 bytes. A message is immutable after commit. Its recipient row progresses
from `queued` through `delivered`, `read`, and `acknowledged`; a separate wake job
is `pending`, `leased`, `succeeded`, or `failed`. `not_requested` means no live
recipient run existed at send time. Wake failure never means message failure.

Direct messages remain project-scoped. A message in an owner-created participant
thread can cross projects only when both sender and recipient have frozen bindings
to their exact agent, active assigned task, and derived project. The message keeps
the sender run's origin project/task even though the recipient belongs elsewhere.

### Thread

An ordered conversation around a task, question, conflict, or announcement.
Threads are asynchronous and durable. Direct threads preserve the original
project-scoped participants. An owner may additionally create a participant
thread with two through eight exact agent/task bindings spanning at least two
projects; no agent or task can appear twice. The owner can then invite one
participant at a time with an expected roster revision.
Every message still has one recipient; the roster is authority, not a broadcast
list. Agents cannot create or invite. Closing and agent-visible roster reads remain
planned. A thread may later be promoted into a meeting when structured
multi-party resolution is needed.

### Meeting

A bounded coordination procedure, not merely a group chat. It has an agenda,
participants, facilitator, input snapshot, ordered rounds or contributions,
resolution criteria, final record, and action items.

### Artifact

A typed pointer to evidence or output: a file, diff, commit, test result, log
excerpt, plan, patch, report, external URL, or content-addressed blob. Crewfold
stores metadata and only copies content when retention policy requires it.

### Knowledge item

A stable, scoped statement intended for reuse. The implemented types are
`decision` and `finding`; briefs, constraints, glossary entries, risks, runbooks,
and summaries remain planned. An item belongs to one workspace and project and may
be narrowed to one task. Its project is derived from its primary provenance source,
not selected independently by retrieval.

### Knowledge revision

A numbered immutable-content snapshot with a `krev_...` ID. It records title, body,
content hash, confidence, verification, freshness, and an optional predecessor.
It has one primary source and up to 15 supporting sources, frozen at their source
revisions. Implemented sources are a task, a concluded meeting, or an accepted
meeting proposal from a concluded meeting; every source must share the item's
workspace and project.

Review state (`proposed|accepted|rejected`) is separate from currency
(`pending|current|stale|superseded`). Content never changes after proposal;
governance advances a state revision. Accepting a proposed successor atomically
makes it current and preserves its prior current revision as superseded history.

The local owner may propose and govern revisions. An authenticated agent run may
propose a decision or finding sourced from its assigned task, but cannot accept,
reject, or stale it. Allowed and denied governance attempts produce durable
authority records.

### Decision

An implemented knowledge type for a governed choice. Its authority comes from an
accepted revision and an allowed governance record: normally the local owner, or
the one exact bounded curator policy described below. It does not come from the
actor that proposed it or from its presence in a model response. Rich structured
alternatives, consequences, and decision-owner policies remain future extensions
to the concise title/body contract.

### Knowledge search result

A read-only, evaluated candidate set over exact canonical revisions. It records
the normalized literal query, evaluation time, canonical event cursor, named rank
policy, derived-index generation/digest, and ordered matches. Every match contains
the complete exact revision plus separately explained applicability, authority,
freshness, provenance, quality, text score, and tie breaker.

Search eligibility requires accepted, current, fresh knowledge in one workspace
and project. A task query permits project-wide plus exact-task applicability; a
project-only query cannot expose task-scoped records. The result grants no
authority and does not become a context input unless a later explicit operation
copies an exact revision ID into a new immutable record.

### Curator rule and derivation

A curator rule is an immutable configuration revision in one workspace. The only
implemented rule is `accepted_meeting_resolution_copy/v1`; every workspace starts
with it disabled at revision one. A derivation is append-only evidence that this
exact rule version rendered one accepted meeting-proposal revision into one exact
proposed knowledge revision. It freezes source and output hashes and grants no
authority by itself.

The curator queue is not a stored entity. It is a read projection over proposed
knowledge plus derivations and the effective rule. Automatic acceptance has its
own immutable record linking enabled evaluation rule revision, derivation,
knowledge revision, authority check, normal acceptance event, and curator event.
Only the narrow exact-copy state policy may use actor `subsystem:curator`; general
subsystem and agent governance remains denied.

### Knowledge contradiction and derived dispute

A `kcon_...` record preserves one globally unique lexical pair of different exact
knowledge revisions. Both must be accepted/current in the same project and their
project-wide/task applicability must intersect. Its independent lifecycle is
`proposed -> open -> dismissed|resolved`, or direct `proposed -> dismissed`; it
never changes either revision's review/currency axis.

Owner or live run actors may report, but only owner confirmation makes the record
effective. A run reporter is bound to its exact workspace/project/task and may see
only broad or same-task participants. `knowledge dispute` is not stored: it derives
the total and a bounded sorted ID sample from incident open records. Each open
record quarantines both complete revisions everywhere they otherwise apply.
Dismissal closes it; stale/supersede governance resolves it atomically with the
exact cause event. A revision remains disputed while any other incident record is
open.

### Portable project knowledge bundle

A `kbun_...` bundle is a deterministic two-file snapshot of one exact project's
canonical knowledge. Its manifest includes exact workspace/project identity,
portable task-scope anchors, all knowledge items and revision histories, ordered
sources, record counts, and final contradiction lifecycle snapshots. It excludes
the origin event and authority ledgers, operational source entities, retrieval
indexes, context, messages, transcripts, credentials, and provider state.

A task-scope anchor binds an opaque exact task ID, workspace/project, and task
creation time/actor identity solely for applicability. It does not create a task
or grant scheduling, source, agent, run, or capability authority. An operational
task with that ID activates exact applicability only when the identity fields
match. An import receipt records the local owner's atomic attestation of one exact
validated bundle. Same-bundle replay is idempotent; different or partial state is
a conflict rather than an implicit merge.

### Outcome assessment

A revisioned judgment about whether a promised deliverable was achieved. It is
distinct from activity, a run result, and task lifecycle state. An assessment
contains:

- the objective, task, and deliverable commitment it evaluates;
- review state `proposed`, `accepted`, `rejected`, or `superseded`;
- conclusion `achieved`, `partial`, `not_achieved`, or `unknown`;
- the delivered scope and any declared compatibility or stability effects;
- material decision and rationale references;
- evidence and verification references, including strength and freshness;
- residual risks, disputed claims, unknowns, and follow-up work;
- assessor, authority, revision, and effective timestamp.

Agents and automated checks may propose assessments. Policy or an authorized
reviewer accepts or rejects the assessment. Separating review state from
conclusion allows an authority to accept a `partial` or `not_achieved` finding
without pretending the deliverable succeeded. A completed run is therefore never
sufficient by itself to assert that an outcome was delivered.

### Management briefing

A revisioned, derived projection over objective, task, outcome, decision,
evidence, check, risk, overlap, and message records. A briefing declares its scope,
event cursor, “as of” time, and optional base checkpoint. Its structured sections
cover commitments, accepted delivery, deviations, rationale, verification,
compatibility and stability, risks and unknowns, required decisions, and proposed
next actions.

A briefing is not a new source of truth and is not an agent-authored summary blob.
Every material claim refers to the durable records from which it was derived. An
optional narrative rendering may improve readability but cannot add facts or hide
conflicts in the structured projection.

### Event

An immutable fact that a domain mutation was accepted or an observation recorded.
Events are ordered by a local monotonically increasing sequence and also carry
opaque IDs and wall-clock timestamps.

## Actor model

Mutations identify one actor:

- human owner;
- agent definition plus run;
- Crewfold subsystem such as scheduler or watcher;
- runtime or external integration;
- future remote user/service identity.

The actor and the authority under which it acted are recorded separately. A
manager agent cannot inherit the owner's authority merely because the owner
launched it.

## Key invariants

1. One active run belongs to exactly one durable agent definition.
2. A task has at most one primary assignment lease, though it can have reviewers
   and collaborators.
3. A completed task cannot have an active primary assignment.
4. Intersecting claims from different tasks produce one deterministic overlap;
   configured policy decides whether to notify, deny the new claim, pause new run
   scheduling, or require resolution.
5. Knowledge revision content and provenance are immutable; correction creates an
   explicit successor revision. Acceptance requires either the owner or the one
   transactionally revalidated exact curator state policy—never proposal labels,
   free text, retrieval rank, or general subsystem identity.
6. A context packet is immutable after build. A prebuilt v4 packet is revalidated
   once when a run binds it; after binding, reads do not re-evaluate its historical
   bytes. A delta never mutates that base and is acknowledged only by its exact
   bound live run.
7. A message is never modified after send; delivery metadata changes separately,
   and cross-project visibility requires an exact participant agent/task/project
   binding rather than workspace membership alone.
8. Runtime termination cannot delete durable task or communication state.
9. Every privileged action is attributable to an actor and policy decision.
10. Provider-specific fields remain in adapter metadata, not core required fields.
11. Run activity cannot imply an accepted outcome without an explicit assessment.
12. Every material management-briefing claim is traceable to durable source
    records at a declared event cursor.
13. A portable import cannot create operational work or replay origin authority;
    only its local-owner import attestation authorizes the imported final state.
14. A live-context bound is never satisfied by truncation. An unsafe or oversized
    incremental change becomes durable rebase state.
15. Agent role strings are owner-defined descriptive labels, never an authority
    taxonomy. Only an authenticated packet-v5 run with a current exact owner grant
    may submit the proposal kinds in that grant, and it may neither accept a
    proposal nor decide an approval.
16. Proposal submission is operationally inert. Owner acceptance has exactly one
    decision/application receipt and applies all revalidated actions or none.
17. Scheduling commits assignment, claims, context/run intent, worker job,
    supervisor action, event, receipt, and idempotency result before runtime launch.
    That receipt freezes authority for the one committed launch: later profile
    retirement, agent disablement or revision change, or lease-deadline passage
    affects future placements, not worker recovery of the already receipted run.
18. Reserved and `lost` runs consume every applicable capacity. Lease expiry never
    makes their tasks ready before runtime reconciliation, and a lost run cannot be
    automatically retried, released, or reassigned.
19. Authority-significant proposal and scheduling facts live in typed constrained
    rows; canonical JSON hashes are corroboration, not authority by themselves.
