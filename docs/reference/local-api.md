# Local API v1

Status: implemented for daemon health, durable workspaces/events, read-only Git
inspection, and provider-neutral agent/objective/task coordination. Subscriptions,
runs, messages, claims, and knowledge arrive later.

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
- the result of `PRAGMA quick_check(1)`.

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

`run.start` takes `workspace`, task ID, optional checkout ID, runtime, provider,
one validated fake scenario, `expected_task_revision`, and `idempotency_key`. It
requires the task to be `assigned` with a current lease. The assigned agent must
be enabled, below `max_concurrency`, and configured for the exact opaque
runtime/provider pair.

Placement is project-scoped and source-layout neutral. An eligible checkout is
available, writable, and has write-policy capacity. `shared` allows concurrent
runs; `exclusive` and `claimed` reject another live run. With no explicit
checkout, selection is deterministic by write-policy preference, normalized path,
and stable ID. The committed run contains the chosen task, agent, checkout path,
write mode, adapter pair, and human-readable reasons. Creating a run and its
pending worker job is one transaction; no adapter call occurs in that transaction.

The daemon worker leases pending jobs and applies this durable lifecycle:

```text
requested -> starting -> active -> completed
                         |   |----> blocked -> active (resume)
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

`run.show` takes `workspace` and run ID. `run.list` accepts optional task and
status filters. Both return run, current task/agent/checkout projections, the run
timeline, and an accepted handoff when present. `run.resume` requires the observed
run revision and resumes a blocked run or an explicitly paused active run from its
persisted cursor. `task.timeline` returns the task, all its runs, and ordered
normalized run/timeline facts.

The runtime driver's `Launch` operation is idempotent by stable run ID. A restart
after committed intent or after runtime launch reclaims the durable job and
returns/reconciles the same runtime binding rather than inventing a second effect.
The current implementation ships only deterministic in-memory fake adapters; a
real subprocess boundary follows separately.

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
