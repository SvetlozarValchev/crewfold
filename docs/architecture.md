# Architecture

## Overview

Crewfold is a local daemon with several clients and replaceable adapters. The
daemon owns durable coordination state. Clients never infer that state solely from
terminal buffers or provider session logs.

```mermaid
flowchart TB
    subgraph Clients
        CLI[CLI]
        TUI[TUI]
        AgentMCP[Agent MCP clients]
        FutureWeb[Future web console]
    end

    subgraph ControlPlane[Local Crewfold control plane]
        API[Local API and MCP server]
        Commands[Command handlers]
        Journal[Event journal]
        Projections[Read projections]
        Scheduler[Scheduler]
        Supervisor[Supervisor]
        Curator[Context curator]
        Outcomes[Outcome and briefing projector]
        Watchers[Git and runtime watchers]
    end

    subgraph Adapters
        HerdrDriver[Herdr runtime driver]
        DirectDriver[Direct process driver]
        ProviderBridge[Provider bridges]
        External[Future CI/issue adapters]
    end

    DB[(SQLite WAL)]
    Git[(Git checkouts)]
    Providers[Codex / Claude Code / others]

    CLI --> API
    TUI --> API
    AgentMCP --> API
    FutureWeb -. deferred .-> API
    API --> Commands
    Commands --> Journal
    Journal --> Projections
    Journal --> Scheduler
    Journal --> Supervisor
    Journal --> Curator
    Journal --> Outcomes
    Watchers --> Commands
    Journal --> DB
    Projections --> DB
    Scheduler --> HerdrDriver
    Scheduler --> DirectDriver
    HerdrDriver --> Providers
    DirectDriver --> Providers
    ProviderBridge --> Providers
    Watchers --> Git
```

## Deployment topology

The first deployment contains one binary and one database:

```text
crewfold daemon
  ├─ owner-only Unix socket: local API plus run-scoped JSON-RPC/MCP
  ├─ SQLite database in WAL mode
  ├─ runtime-driver processes or CLI calls
  └─ bounded background loops: scheduler, watchers, curator queue
```

The CLI can start the daemon on demand. A foreground mode makes debugging easy; a
user service may keep it running. The same binary can expose CLI subcommands so
installation remains simple.

Default state locations should follow XDG conventions on Linux:

```text
~/.config/crewfold/config.yaml
~/.local/share/crewfold/crewfold.db
~/.local/state/crewfold/crewfold.log
~/.cache/crewfold/
```

No daemon state should be written into a managed repository unless the owner
explicitly initializes a project-local `.crewfold/` configuration directory.

## Command path

All mutations follow one path:

```mermaid
sequenceDiagram
    participant Caller as Human or agent
    participant API as Local API
    participant Policy as Policy evaluator
    participant Handler as Command handler
    participant DB as SQLite transaction
    participant Worker as Scheduler/supervisor

    Caller->>API: command + actor + idempotency key
    API->>Policy: authorize(action, scope)
    Policy-->>API: allow / approval required / deny
    API->>Handler: validated command
    Handler->>DB: append event + update projection atomically
    DB-->>Caller: result + event cursor
    DB-->>Worker: durable work becomes visible
```

Runtime observations enter through the same validated command path. There is no
special back door by which terminal text can mutate task or knowledge state.

## Core modules

### Local API

Provides commands, queries, and subscriptions over a user-owned Unix socket. The
wire contract is versioned and described by JSON Schema. A loopback TCP listener
is not enabled by default.

The API supports:

- idempotency keys for mutations;
- optimistic concurrency through expected revision numbers;
- event cursors for watch/resume;
- actor identity on every command;
- machine-readable errors and human-readable explanations.

### MCP server

Exposes a safe subset of Crewfold capabilities to coding agents. It translates MCP
tool calls into the same internal commands used by the CLI. MCP is not the daemon's
storage model and does not receive privileged actions by default.

The implemented server recognizes MCP envelopes on the existing Unix socket. A
node-secret HMAC token authenticates one run through request metadata; ordinary
tool arguments cannot select a different identity. The database stores capability
expiry and immutable context binding, never the credential. Resource and tool
scope violations are denied and audited.

### Command handlers

Enforce domain invariants, create stable IDs, evaluate expected revisions, and
append canonical events. Handlers do not launch processes while holding a database
transaction. Instead, they record intent for a worker to execute.

### Event journal and projections

Every accepted mutation produces an immutable coordination event. Current-state
tables are updated in the same SQLite transaction for efficient queries. This is
not a commitment to a large event-sourcing framework; it is a compact audit journal
plus rebuildable projections.

### Scheduler

Matches ready tasks with eligible agent definitions, available checkouts, runtime
capacity, and policy. It produces an explainable placement proposal or a blocked
reason. Process launch happens through a runtime driver after the placement is
committed.

The implemented deterministic scheduler consumes the task's existing active
assignment, verifies the agent's enabled state and runtime/provider configuration,
enforces agent concurrency, and selects a writable checkout in the task's project.
Checkout eligibility depends on availability and write policy, not Git layout:
adjacent clones and linked worktrees are equal inputs. The selected placement and
its reasons are durable before the asynchronous worker sees the job.

### Supervisor

Consumes task, run, claim, message, and watcher events. It applies deterministic
rules first and may ask a manager model for a recommendation when judgment is
useful. Recommendations do not gain more authority because a model generated them.

### Context curator

Maintains the difference between evidence and accepted shared knowledge. It can
extract proposed decisions, findings, risks, and summaries from structured agent
reports. Automatic acceptance is limited to low-risk scopes and explicit rules.

Before canonical knowledge exists, the implemented packet builder is deliberately
deterministic: it snapshots only current role, task, checkout, dependency, policy,
and reporting facts and records explicit exclusions for knowledge, messages,
claims, and transcripts. Equivalent inputs have one semantic hash; packets remain
immutable and single-run-bound. This is a bounded context authority, not RAG or a
transcript accumulator.

### Outcome and briefing projector

Builds the management view from structured commitments, outcome assessments,
decisions, checks, evidence, risks, overlaps, and follow-up work. Its base
aggregation is deterministic and available even when no manager model or curator
is running. It supports both an “as of” event cursor and a change view since an
owner checkpoint.

The projector preserves provenance and source authority for every material claim.
It explicitly marks weak, stale, missing, disputed, or contradictory support. A
manager model may render or recommend from this projection, but model prose cannot
alter its facts, silently reconcile conflicts, or become the only representation
of project state. Session transcripts are optional evidence and are not required
to build the projection.

### Watchers

Watchers observe:

- provider/runtime lifecycle;
- Git branch, HEAD, working-tree changes, and touched paths;
- local check processes;
- timers, leases, budgets, and stale heartbeats.

They emit observations with provenance. Reconciliation logic decides what those
observations mean for durable state.

M3's synchronous Git observer is the first watcher seam. It accepts a concrete
directory and never assumes `git worktree` ownership: adjacent clones and linked
worktrees are both checkouts. It executes only bounded `rev-parse`, `rev-list`, and
`status` commands with optional locks, filesystem-monitor hooks, untracked-cache
writes, automatic maintenance, and terminal prompting disabled. Repository
identity comes from history observations; checkout identity comes from the
normalized local path and a durable opaque ID.

### Runtime drivers

Runtime drivers create, attach, prompt, observe, and stop execution environments.
Herdr is the first-class interactive driver. A direct-process driver supports
headless tools, tests, and deterministic integration tests.

### Provider bridges

Provider bridges add capabilities beyond generic terminal interaction, such as
native resume identifiers or lifecycle hooks. The core only depends on normalized
capabilities and events.

## Sources of truth

| Concern | Authority |
| --- | --- |
| Source contents and commit graph | Git and filesystem |
| Terminal topology and live pane buffers | Runtime driver, initially Herdr |
| Provider-private conversation/session | Provider tool |
| Tasks, assignments, claims, messages, meetings | Crewfold database |
| Accepted project knowledge and decisions | Crewfold knowledge records |
| Accepted deliverables and residual outcome risk | Crewfold outcome assessments |
| Verification results | Check systems and evidence records, with source and freshness |
| Management briefing | Derived Crewfold projection; never an independent authority |
| Authorization and autonomy limits | Crewfold policy configuration |
| External CI or issue status | External system, cached as observation |

Crewfold stores identifiers and evidence pointers across these boundaries. It does
not pretend that a cached observation is the external system itself.

## Consistency and failure handling

### Transactional intent, asynchronous effect

Launching an agent is a saga:

1. Commit `run.requested` with task, agent, checkout, and limits.
2. Runtime worker attempts the launch with an idempotency token.
3. Persist the runtime binding before the post-launch acknowledgement boundary.
4. Commit `run.started` with provider binding, or `run.start_failed` when launch
   definitely did not occur.
5. Reconciliation inspects the persisted binding after a crash; an untrustworthy
   process outcome becomes `run.lost` without releasing capacity.

If provider binding fails after a runtime exists, the worker first stops that
known runtime. It records a normal start failure only after cleanup is confirmed;
otherwise it records `run.lost` and preserves the assignment and checkout claim.

The fake runtime currently proves steps 1–3 and replay-safe recovery at the
post-effect/pre-acknowledgement boundary. Its operation ID is the durable run ID;
replaying launch returns the same binding. Requested intents, blocked runs, and
active checkpoints also survive daemon restart through the SQLite worker queue
and persisted scenario cursor.

The direct runtime additionally proves restart across a real process boundary. A
detached per-run supervisor persists process identity, bounded output counts, and
exit/timeout/stop state in owner-only daemon storage. A freshly constructed driver
can reconcile that state after daemon restart while the child continues. The
runtime never treats terminal text or exit zero as task-completion authority; the
fixture provider must still emit a structured completion report and pass domain
acceptance. Current process identity and process-group enforcement are Linux-first.

The same pattern applies to prompts, stops, meetings, and external actions.

### Leases

Assignments and claims use renewable leases. Expiry makes abandonment visible but
does not silently erase evidence or assume that a process stopped writing. A
supervisor reconciles expired ownership before reassignment.

### Idempotency

Every side-effect request has a stable operation ID. A runtime adapter must return
the existing result or a reconciled state when the same request is repeated after
a crash.

### Backpressure

Queues are durable and bounded. Background work has concurrency classes. New
launches stop before status collection, message delivery, or lease renewal becomes
unreliable.

## Extension boundary for organization mode

The organization product would replace or extend the local API transport,
identity, database, scheduler placement, and audit retention. It should reuse the
domain concepts, protocol semantics, adapter SDK, and local node. The local daemon
can eventually become an authenticated edge worker rather than being discarded.

That evolution depends on never using filesystem paths or operating-system UIDs as
globally meaningful identities. Durable IDs are opaque; paths and local users are
attributes scoped to a node.
