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
capabilities, team membership, project eligibility, and action policy arrive with
the capabilities that consume them. Provider/runtime values remain opaque data.

### Run

One concrete attempt to execute work using an agent definition. It binds:

- an agent definition and provider adapter;
- a runtime driver and runtime handle;
- zero or one primary task;
- a checkout and context-packet revision;
- cost/time/resource budgets;
- timestamps, heartbeats, result, and termination reason;
- a provider resume handle when supported.

Run state:

```text
requested -> starting -> active -> settling -> completed
                      \-> blocked
                      \-> failed
                      \-> stopping -> stopped
```

`blocked` is resumable and not a terminal result. Runtime-observed state and
Crewfold run state are related but not identical.

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
draft -> ready -> assigned -> active -> review -> completed
                    |          |          |
                    +-------> blocked <---+
                    +-------> cancelled
                    +-------> failed -> ready (explicit retry)
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

Claims can be shared-read, exclusive-write, or advisory. A claim has a lease,
rationale, expected duration, and task. Observed Git changes are compared with
claims; they do not retroactively become authorization.

### Message

A durable envelope from one actor to one or more recipients. It has a message kind,
thread, urgency, acknowledgement requirement, optional task or artifact links, and
delivery/read/acknowledgement state.

Messages contain concise coordination content. Large evidence lives in artifacts
or source systems and is linked.

### Thread

An ordered conversation around a task, question, conflict, or announcement.
Threads are asynchronous and durable. A thread may be promoted into a meeting when
structured multi-party resolution is needed.

### Meeting

A bounded coordination procedure, not merely a group chat. It has an agenda,
participants, facilitator, input snapshot, ordered rounds or contributions,
resolution criteria, final record, and action items.

### Artifact

A typed pointer to evidence or output: a file, diff, commit, test result, log
excerpt, plan, patch, report, external URL, or content-addressed blob. Crewfold
stores metadata and only copies content when retention policy requires it.

### Knowledge item

A versioned, scoped statement intended for reuse. Types include brief, constraint,
decision, glossary entry, finding, risk, runbook, and summary. Each revision has
provenance, authority, confidence, freshness, and supersession relationships.

### Context packet

An immutable record of exactly what Crewfold selected for a run or meeting:

- objective and task contract;
- role and policy;
- relevant accepted knowledge;
- active claims, dependencies, and recent messages;
- selected evidence links;
- retrieval reasons and size estimates.

Packets enable reproducibility and explain why an agent knew—or did not know—a
fact.

### Decision

A special authoritative knowledge item with status `proposed`, `accepted`,
`superseded`, or `rejected`. Decisions record alternatives, rationale, owner, scope,
and consequences.

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
4. An exclusive claim conflicts with every overlapping write claim.
5. Knowledge revisions are immutable; correction creates a new revision.
6. A context packet is immutable after dispatch.
7. A message is never modified after send; delivery metadata changes separately.
8. Runtime termination cannot delete durable task or communication state.
9. Every privileged action is attributable to an actor and policy decision.
10. Provider-specific fields remain in adapter metadata, not core required fields.
