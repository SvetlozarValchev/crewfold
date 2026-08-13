# Local API v1

Status: implemented for daemon health, durable workspaces/events, read-only Git
inspection, provider-neutral agent/objective/task/run coordination, immutable
context packets, owner-facing durable agent mail, leased claims with deterministic
overlap/drift inspection, structured overlap-resolution meetings, canonical
knowledge, and deterministic derived retrieval. Subscriptions arrive later.

## Transport

The daemon listens on an explicitly selected Unix domain socket. It accepts one or
more newline-delimited JSON requests per connection and emits one response line per
request.

The socket is created with mode `0600`. The daemon holds an advisory exclusive lock
with mode `0600` on `<data-dir>/daemon.lock` so another daemon cannot use the same
data directory, even with a different socket. A newly created data directory uses
mode `0700`; Crewfold does not silently change the mode of an existing directory.

There is no network listener or remote transport.

## Negotiation

A client begins each connection with `system.hello` and declares its supported
inclusive range:

```json
{"id":"req-1","method":"system.hello","params":{"min_protocol":1,"max_protocol":1}}
```

A compatible daemon selects the highest shared protocol:

```json
{
  "id": "req-1",
  "protocol": 1,
  "result": {
    "type": "hello",
    "selected_protocol": 1,
    "server_min_protocol": 1,
    "server_max_protocol": 1,
    "version": {
      "schema": "urn:crewfold:schema:cli:version-response:v1",
      "version": "dev",
      "commit": "unknown",
      "built_at": "unknown",
      "go_version": "go1.26.5",
      "platform": "linux/amd64"
    }
  }
}
```

If the ranges do not overlap, the daemon returns `protocol_mismatch` with its
supported minimum and maximum. Non-hello requests must carry the selected
`protocol`.

## Envelope

Request:

```json
{"id":"req-2","protocol":1,"method":"system.status"}
```

Success:

```json
{"id":"req-2","protocol":1,"result":{"type":"system_status"}}
```

Failure:

```json
{
  "id": "req-2",
  "protocol": 1,
  "error": {
    "code": "method_not_found",
    "message": "unknown local API method",
    "retryable": false
  }
}
```

The response ID must equal the request ID. Exactly one of `result` or `error` is
present. Request IDs contain 1–128 characters and serve as log correlation IDs;
they are not authorization credentials.

Published JSON Schemas live under `protocol/schemas/local/v1/`.

## Methods

### `system.hello`

Negotiates a protocol and returns the daemon build information. This request does
not carry a `protocol` because selection is its purpose.

### `system.status`

Returns:

- status and selected protocol;
- daemon PID and start time;
- monotonic-derived uptime in milliseconds;
- server build information;
- current request count and whether shutdown is pending.

The status is process health only. Database health is reported separately so a
healthy process cannot hide a failed storage check.

### `system.stop`

Returns `status: stopping`, then closes the listener and all accepted connections.
The server waits for handlers to exit and removes only the socket file it created.

An idle or partially written client cannot hold shutdown open indefinitely.

### `database.status`

Takes no parameters. It reports:

- current and latest embedded schema versions;
- SQLite journal mode (`wal` is required);
- whether foreign-key enforcement is active;
- global physical/canonical SQLite integrity from `PRAGMA quick_check(1)` on a
  short-lived connection without FTS5 registration. It checks page allocation,
  freelist state, and ordinary B-trees without invoking the disposable virtual
  table's semantic integrity hook. Retrieval projection health remains available
  through `knowledge.index.status` and `doctor --retrieval`.

The CLI exposes this as `crewfold doctor --database --socket <path>`.

### `workspace.init`

Atomically creates the first workspace projection, appends one
`workspace.created` event, and records the successful response under an
idempotency key:

```json
{
  "id": "req-3",
  "protocol": 1,
  "method": "workspace.init",
  "params": {
    "name": "personal",
    "idempotency_key": "initialize-personal"
  }
}
```

Workspace names start with a lowercase letter and contain at most 63 lowercase
letters, digits, or hyphens. Repeating the same key and normalized command returns
the stored result without appending another event. Reusing the key for another
payload returns `idempotency_conflict`. A duplicate name under a new key returns
`workspace_already_exists`; neither failure changes a projection or event.

The request ID becomes the event correlation ID. The successful result contains
the complete workspace record plus its event ID and local sequence.

### `workspace.show`

Queries one workspace by stable ID first and then by unique name:

```json
{"id":"req-4","protocol":1,"method":"workspace.show","params":{"identifier":"personal"}}
```

A missing record returns `workspace_not_found`.

### `project.add`

Takes `workspace`, `name`, `repository_path`, optional `write_mode`, and
`idempotency_key`. The daemon first performs bounded read-only Git inspection,
then atomically creates a project, attaches or creates a repository identity,
creates a checkout, appends events, and stores the idempotent result. The default
write mode is `exclusive`.

The path can be a standalone repository, an adjacent clone, or a linked worktree.
Git failure occurs before the transaction, so `not_git_repository`,
`git_unavailable`, or `git_output_invalid` creates no partial project.

### `checkout.add`

Takes `workspace`, `project`, `repository_path`, optional `write_mode`, and
`idempotency_key`. Every normalized path receives its own checkout ID. If its
history fingerprint is already known in the workspace, the existing repository
ID is reused—even when the directories do not share Git worktree metadata.
Duplicate normalized paths return `checkout_already_registered`.

### `checkout.list`

Takes `workspace` and `project`. It returns stored checkout projections without
invoking Git, so unavailable locations remain visible.

### `project.inspect`

Takes `workspace` and `project`. It refreshes each stored path using read-only Git
commands. Changed observations increment only the affected checkout revision and
append `checkout.git_observed`. A missing/moved path becomes `unavailable` with a
diagnostic while retaining its checkout ID, repository ID, registered path, and
last-known branch/HEAD state.

### Agent definitions

`agent.create` takes `workspace`, `name`, `role`, `provider`, optional `runtime`,
optional `max_concurrency`, and `idempotency_key`. Runtime defaults to
`unconfigured`, concurrency to one, and the definition starts enabled. Provider
and runtime are opaque capability/configuration strings; neither selects a core
code branch or launches a process.

`agent.update` takes `workspace`, an agent ID or name, one or more mutable fields,
`expected_revision`, and `idempotency_key`. `agent.show` accepts an ID or name;
`agent.list` returns definitions ordered by name and ID.

### Objectives

`objective.create` takes `workspace`, `project`, `title`, an explicit `budget`
object, and `idempotency_key`. A budget has non-negative `token_limit`,
`cost_cents`, and `time_seconds`; zero means unlimited/not enforced for that
dimension. `objective.update` atomically replaces supplied title, status, or
budget fields and requires `expected_revision`. `objective.show` queries by ID;
`objective.list` scopes results to a project.

Objective status is `active`, `completed`, or `cancelled`. This layer records the
owner's coordination intent; it does not launch work or automatically cascade a
status change to tasks.

### Tasks, dependencies, and assignments

`task.create` takes `workspace`, `project`, optional objective ID, title, optional
description, priority from 0 through 1000, budget, and `idempotency_key`. The
objective, when present, must be active and belong to the selected project. New
tasks begin at revision 1 in `ready` state.

`task.update` changes title, description, priority, or the complete budget and
requires `expected_revision`. `task.dependency.add` adds a same-project edge,
rejects duplicate/self/circular edges, and increments the dependent task's
revision. Dependencies can be added only while that task is ready and unassigned.

`task.assign` takes a task, agent ID or name, lease length from one second through
30 days, and `expected_revision`. It requires an enabled agent and derived-ready
task. A partial unique index and transaction invariant allow only one active
primary assignment per task. It creates a durable assignment record and changes
the task to `assigned`; it does not start a runtime.

`task.transition` currently accepts `start`, `block`, `unblock`, or `cancel`:

- `start` changes `assigned` to `active` as coordination state only;
- `block` requires a reason and accepts ready, assigned, or active tasks;
- `unblock` returns to assigned while a lease remains active, otherwise ready;
- `cancel` releases an active assignment but retains its history.

`task.show` and `task.list` return task details, dependency edges, any active
assignment, and derived readiness. `ready_only` filters deterministically by
priority descending, then creation time and ID. Readiness is false unless status
is `ready`; an incomplete dependency names its ID and state in the explanation.

Before task/status queries, expired leases are reconciled transactionally.
Assigned or active tasks return to ready, blocked tasks remain blocked, the
assignment changes to `expired`, the task revision advances, and
`task.assignment_expired` is appended. No assignment history is deleted.

All mutations are idempotent. Updates and transitions use optimistic revisions;
two writers using the same revision yield exactly one success and one
`revision_conflict` after serialization.

### Runs, placement, and timelines

`context.build` takes workspace, assigned task, its assigned agent, optional
checkout, expected task revision, and idempotency key. It creates a bounded
immutable packet that fixes role/task/checkout revisions, direct dependencies,
policy, reporting instructions, and explicit included/excluded explanations.
`context.show` returns that exact packet; `context.explain` returns its stable
selection reasons, semantic hash, and byte size.

New builds return context-packet/result schema v2 with a bounded inbox snapshot.
`context.show` result v2 can carry a preserved v1 packet created before mailbox
support or a current v2 packet; it never upgrades the stored packet in place.

`run.start` takes `workspace`, task ID, optional checkout ID, optional context
packet ID, runtime, provider, one validated deterministic scenario,
`expected_task_revision`, and
`idempotency_key`. It
requires the task to be `assigned` with a current lease. The assigned agent must
be enabled, below `max_concurrency`, and configured for the exact opaque
runtime/provider pair.

Placement is project-scoped and source-layout neutral. An eligible checkout is
available, writable, and has write-policy capacity. `shared` allows concurrent
runs; `exclusive` and `claimed` reject another live run. With no explicit
checkout, selection is deterministic by write-policy preference, normalized path,
and stable ID. The committed run contains the chosen task, agent, checkout path,
write mode, adapter pair, and human-readable reasons. Creating a run and its
pending worker job, context binding, and expiring capability is one transaction;
no adapter call occurs in that transaction. Without an explicit packet, the same
transaction creates one. An explicit packet must match current task, agent, and
checkout revisions and cannot be reused by another run.

The daemon worker leases pending jobs and applies this durable lifecycle:

```text
requested -> starting -> active -> completed
                         |   |----> blocked -> active (resume)
                         |   |----> stopping -> stopped
                         |   |----> lost
                         |   |----> review / task changes_requested
                         |   \----> failed
                         \--------> start_failed
```

The fake provider emits normalized `progress`, `blocked`, or `completion`
observations from a bounded scenario. Progress advances the persisted cursor.
Blocked observations require a question and pause the job. Completion records a
proposal and evaluates required evidence. Accepted completion releases the task
assignment and creates one durable handoff; rejected completion retains the
assignment and leaves the run in `review` with the task in `changes_requested`.
A runtime start failure leaves the task assigned so the owner can retry or
reassign it.

With `runtime=direct` and `provider=fixture`, the scenario runs in a real local
subprocess supervised outside the daemon lifecycle. The working directory is the
selected checkout and cannot be overridden through `run.start`. Environment
inheritance and stdout/stderr are bounded. Completion waits for the process to
settle; non-zero exit, timeout, and an untrustworthy process outcome stay distinct.

`run.show` takes `workspace` and run ID. `run.list` accepts optional task and
status filters. Both return run, current task/agent/checkout projections, the run
timeline, and an accepted handoff when present. `run.resume` requires the observed
run revision and resumes a blocked run or an explicitly paused active run from its
persisted cursor. `task.timeline` returns the task, all its runs, and ordered
normalized run/timeline facts.

`run.logs` takes `workspace`, run ID, and a line-tail bound. It returns runtime
state plus independently capped stdout/stderr, captured/omitted byte counts, and
truncation flags. Secret-like values are redacted from the API result.

`run.stop` takes `workspace`, run ID, `expected_revision`, a grace period from 1
to 30,000 milliseconds, and `idempotency_key`. It moves an active/blocked run to
`stopping`; the worker requests graceful process-group termination and then a
forced fallback. The terminal `stopped` record preserves whether force was used,
returns the task to `assigned`, and retains its lease. If process identity cannot
be trusted, the run becomes `lost`, the task remains blocked, and capacity is not
released automatically.

The runtime driver's `Launch` operation is idempotent by stable run ID. A restart
after committed intent or after runtime launch reclaims the durable job and
returns/reconciles the same runtime binding rather than inventing a second effect.
The current implementation ships deterministic in-memory fake adapters plus the
real direct/fixture subprocess boundary. Real model-provider adapters follow
separately.

The same socket also recognizes JSON-RPC 2.0 MCP envelopes; these do not use local
API protocol negotiation. The MCP contract and authentication boundary are
documented separately in [MCP tools](mcp-tools.md).

### Messages and threads

`message.send` takes `workspace`, one `recipient_agent`, `kind`, `body`, and an
idempotency key. It optionally accepts `subject`, `thread`, `project`, `task`,
`artifact_ids`, and `reply_to_message`. The local API actor is the owner. It can
create unscoped or project/task-scoped mail for one enabled agent but cannot attach
run-owned artifacts. A body contains 1 through 4096 bytes of valid UTF-8; at most
16 unique artifact IDs are accepted. Sending and queuing its recipient commit in
one transaction.

`inbox.list` takes `workspace`, agent ID/name, and a limit from 1 through 50. It is
an owner inspection query and does not advance delivery state. `thread.show` takes
`workspace` and thread ID and returns its ordered messages and delivery records,
including acknowledgement and separate wake status/diagnostic.

The owner surface deliberately has no read/acknowledge mutation on behalf of an
agent. Those transitions require an authenticated live run through MCP. The local
socket remains owner-only and is not itself the run authorization boundary.

### Claims, overlaps, and drift

`claim.add` takes `workspace`, `project`, task ID, one `kind`/`target`, optional
checkout, optional mode/policy, lease seconds, and an idempotency key. Kinds are
`path`, `component`, and `operation`; modes are `exclusive`, `shared`, and
`advisory`; conflict policies are `notify`, `deny_new`, `pause_scheduling`, and
`request_resolution`. Mode defaults to exclusive and policy to notify. Leases are
one second through 30 days.

Path targets are normalized repository-relative globs containing literals, `*`,
`?`, and whole-segment `**`. They require one available writable checkout; the
caller must choose it when a project has multiple candidates. The daemon first
refreshes that checkout using bounded read-only Git inspection so the claim stores
an actual dirty-path baseline. Semantic component/operation claims compare exact
case-sensitive labels and need no checkout.

All active same-kind claims in the project are compared across tasks. A path
intersection algorithm returns a concrete path that matches both declarations;
it does not use embeddings. Severity is low when either mode is advisory,
critical for exclusive/exclusive, high for exclusive/shared, and medium for
shared/shared. Effective policy precedence is deny, pause, request resolution,
then notify. `deny_new` returns `claim_conflict` with no partial projection/event.
Pause creates durable holds checked by `run.start`; release or lease expiry
resolves affected overlaps and removes their holds.

`claim.list` filters by optional project and `active|expired|released` status.
`claim.release` requires an expected revision and idempotency key. Claim queries,
claim creation, and run scheduling reconcile expired leases first.

`overlap.list` filters by project and `open|resolved`; `overlap.inspect` returns
the two claim/task IDs, witness, severity, effective response, scheduling flags,
and deterministic explanation. `overlap.scan` asks the daemon watcher to inspect
active claimed checkouts immediately and returns per-checkout scan facts plus
bounded issues.

The watcher records HEAD and sorted dirty paths per concrete checkout. It compares
new paths with each task's active path-claim union and baseline. Out-of-scope
paths create drift records without changing claim targets. A watcher ID change
after daemon restart sets `observation_gap`; separate adjacent clones and linked
worktrees retain distinct checkout IDs even if repository identity is shared.
`drift.list` filters durable records by `open|resolved` status. Claims do not
provide filesystem isolation, and a shared checkout produces an explicit warning.

### Structured meetings

`meeting.create` freezes one open overlap, both claims and tasks, the selected
agent definitions, and the current event cursor. It takes two or three distinct
participants, an independent facilitator, a deadline, and one authority policy:
`owner_decision`, `named_reviewer`, or `manager_bounded`. Named-reviewer policy
requires a reviewer. Bounded-manager policy requires an explicit action allowlist.

`meeting.run` consumes a deterministic fixture containing independently authored
positions followed by an optional facilitator proposal. Contributions are unique
per meeting, agent, and round. A retry reuses an identical durable contribution;
a different replacement is rejected. Missing positions and deadlines produce a
durable `stalled` state without discarding received positions. A facilitator can
resume after daemon restart from `facilitator_pending` without asking submitted
participants again.

Proposal actions are typed as `sequence`, `split`, `reassign`, `designate_role`,
or `cancel`. They can reference only the meeting's frozen tasks and agents. Owner
policy records the proposal as `awaiting_approval` and changes no work until
`meeting.accept`. Named-reviewer and bounded-manager policies apply only within
their configured authority. Before applying any action, Crewfold verifies that
the frozen overlap, claims, and task revisions are still current; the complete
action set commits atomically or not at all.

A sequence action adds a real task dependency, releases the downstream task's
overlapping claim, and resolves its coordination hold. `meeting.inspect` returns
the frozen input, participant state, contributions, proposal, and action results.
`meeting.takeover` lets the local owner supply and authorize a typed proposal for
a stalled meeting; it is explicit authority, not silent autonomous recovery.

### Canonical knowledge and retrieval

`knowledge.propose`, `knowledge.show`, `knowledge.list`, `knowledge.accept`,
`knowledge.reject`, and `knowledge.mark_stale` expose the canonical revision and
owner-governance contract described in [Knowledge](../knowledge.md). Mutation
payloads cannot select a trusted actor. Search adds three owner-socket methods
without changing those authority rules.

`knowledge.search` requires `workspace`, `project`, and a bounded literal `query`.
It optionally accepts `task`, `type`, and a limit from 1 through 100. Its result is
`knowledge_search`: normalized query, evaluation instant, canonical event cursor,
`knowledge_search_v1` policy, index generation, and ordered complete revision
matches with tuple explanations. It is read-only and never appends an event or
builds context.

`knowledge.index.status` requires `workspace` and returns
`knowledge_index_status`. Status is `ok` or `degraded`; generation/source metadata
and a bounded diagnosis make missing, corrupt, inconsistent, or out-of-date
projection state inspectable. `knowledge.index.rebuild` requires `workspace` and
an idempotency key. It returns `knowledge_index_rebuild` with the atomically
published status/generation and changes no canonical knowledge or event record.
Healthy post-proposal catch-up may atomically append immutable rows and refresh
the cursor/count/digest within that generation; it does not repair damaged derived
state, and only a full rebuild creates a generation.
The same key replays only while that healthy generation and source digest remain
current. A degraded projection returns `retrieval_degraded`; a later healthy
generation or canonical refresh makes the old key return `idempotency_conflict`,
so a new rebuild uses a new key. `source_event_sequence` and the search result's
`canonical_event_sequence` are global event-journal observation high-water marks,
not retrieval-freshness checks or workspace-scoped knowledge cursors.
The CLI exposes these methods as `knowledge search`, `knowledge index status`,
`knowledge index rebuild`, and `doctor --retrieval`.

### `coordination.status`

Takes `workspace` and returns counts for registered/enabled agents plus
registered, derived-ready, assigned, active, blocked, completed, and cancelled
tasks. The CLI exposes it as `crewfold status --workspace <scope>`; omitting the
workspace retains the process-health form of `status`.

### `events.list`

Returns events in ascending local sequence order strictly after a supplied cursor:

```json
{"id":"req-5","protocol":1,"method":"events.list","params":{"after":0,"limit":100}}
```

The default limit is 100 and the maximum is 1000. `next_after` is the final event
sequence in the page, or the input cursor for an empty page. `has_more` tells the
caller to issue another query from `next_after`. A resumable live subscription
arrives later.

## Socket startup safety

When the requested socket path already exists, Crewfold behaves conservatively:

| Existing path | Behavior |
| --- | --- |
| Reachable Unix socket | Refuse with `socket_in_use` |
| Socket returning connection refused | Recheck identity/type, then remove as stale |
| Regular file, directory, or symlink | Preserve and refuse with `socket_path_occupied` |
| Socket changes during inspection | Preserve and refuse |

The daemon never recursively deletes a socket parent or data directory.

## Logging

Foreground daemon logs are newline-delimited JSON on stderr. Completed request logs
include `component`, `request_id`, `method`, `status`, `duration_ms`, and an error
code when applicable. Logs do not include arbitrary request bodies.

## Limits and deferrals

- Maximum request line: 64 KiB.
- Local operating-system user is the only identity; events use
  the explicit placeholder actor `local-owner` of type `human`.
- Workspace/source registration and durable work coordination are implemented.
  Event cursors, optimistic revisions, leases, and command idempotency are
  durable; subscriptions and streaming are not implemented.
- Unix sockets are the only supported transport; Windows named pipes are later.
- Socket permission is a transport boundary, not future agent authorization.
