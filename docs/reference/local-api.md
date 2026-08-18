# Local API v1

Status: implemented for daemon health, durable workspaces/events, read-only Git
inspection, provider-neutral agent/objective/task/run coordination, immutable
context packets, owner-facing durable agent mail, leased claims with deterministic
overlap/drift inspection, structured overlap-resolution meetings, canonical
knowledge, and deterministic derived retrieval. Resumable bounded event pages are
the current live-client contract; there is no separate streaming representation.
The owner-local surface also exposes the bounded deterministic curator queue,
rule configuration, explicit processing pass, and exact knowledge-contradiction
governance, portable project knowledge snapshots, and explicit bounded live
context refresh and inspection. M16 adds owner-granted manager invocation and
proposal decisions, exact launch profiles, deterministic supervision, and an
owner approval queue. M17 retains protocol v1 and adds owner check-definition,
criterion, exact grant/route/policy, execution/inspection/watch, and repair-
decision methods. M18 retains the same protocol version and adds only the
owner-local commitment, outcome-assessment, checkpoint, and structured briefing
methods documented below. M19 replaces unbounded operator reads with their one
current bounded shape. M20 adds only online full health, online quiescent backup
creation, and owner resolution of a lost runtime; verify, restore, activation,
repair inspection, and load remain offline CLI operations.

## Transport

The daemon listens on an explicitly selected Unix domain socket. It accepts one or
more newline-delimited JSON requests per connection and emits one response line per
request.

The socket is created with mode `0600`. The daemon holds an advisory exclusive lock
with mode `0600` on `<data-dir>/daemon.lock` so another daemon cannot use the same
data directory, even with a different socket. A newly created data directory uses
mode `0700`; Crewfold does not silently change the mode of an existing directory
or lock. Every path component and the lock are opened without following links,
and an existing lock must be owner-held, single-linked, regular, and exact `0600`
before Crewfold writes its PID.

There is no network listener or remote transport.

The bundled client bounds Unix-socket connection establishment at two seconds
and ordinary complete request/response round trips at ten seconds. A shorter
caller context or explicit client timeout wins. The separate longer windows for
portable knowledge and M20 maintenance are documented with those methods; the
ordinary window is long enough to receive SQLite's typed five-second
`database_busy` result rather than replacing it with a transport timeout.

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

### `web.bootstrap`

Takes exactly `{}` over the owner-only Unix socket. It mints one 32-byte random
grant retained only as a digest for 60 seconds and returns:

```json
{
  "schema": "urn:crewfold:schema:local-api:web-bootstrap-result:v1",
  "type": "web_bootstrap",
  "url": "http://127.0.0.1:43121/#bootstrap=<64-lower-hex>",
  "expires_at": "<canonical-timestamp>"
}
```

The fragment is not sent in the initial HTTP request. Workbench JavaScript posts
it once from the exact listener origin to `/api/v1/session`; replay, expiry,
unknown/duplicate fields, another Host, or another Origin fail closed. Successful
exchange clears the fragment, sets an HttpOnly `SameSite=Strict` owner cookie on
one random 256-bit API-path prefix, and returns that prefix plus a separate
in-memory CSRF token. Scoping the cookie to an unguessable path prevents a browser
from attaching it to ordinary requests sent to another service on `127.0.0.1`.
The current authenticated browser API contains bounded status and strict
canonical-RPC reads, cursor-bearing SSE invalidation, repository/provider
onboarding, durable owner/executive exchanges, explicit review of closed typed
manager proposals, fresh bounded Git observation, and one separately granted run-bound
terminal WebSocket. Browser mutations require the exact in-memory CSRF token.
Static assets and the shell are public only to exact loopback Host and disclose
no daemon data before session exchange.

### `owner.crew.configure`

Changes the implementation workers authorized by one active workbench executive
binding. It is an owner-authority mutation available through authenticated
browser RPC and the secondary `crewfold crew` CLI surface. Parameters are one of:

```json
{"workspace":"ws_<id>","project":"prj_<id>","action":"add","expected_binding_revision":3,"name":"reviewer","provider":"codex","runtime":"herdr","max_concurrency":2,"idempotency_key":"owner-crew-4"}
```

```json
{"workspace":"ws_<id>","project":"prj_<id>","action":"disable","expected_binding_revision":4,"agent":"agent_<id>","idempotency_key":"owner-crew-5"}
```

`add` preflights the selected registered provider/runtime and creates an enabled
implementation definition plus immutable project launch profile before replacing
the executive's exact grant/profile binding. It creates no task, assignment, or
run. `disable` requires an exact canonical agent ID, refuses the final worker and
any worker retaining accepted/live work, replaces the executive authority, then
disables the definition and retires its active implementation profiles. Replay
uses the same configuration hash and returns the exact committed result; changed
content is an idempotency conflict. The result schema is
`urn:crewfold:schema:local-api:owner-crew-mutation-result:v1` and returns the
binding, affected agent, complete active worker-profile set, and event sequence.

The listener is exact `127.0.0.1`, sends no wildcard CORS policy, denies framing,
sets `nosniff` and no-referrer policy, and restricts scripts, styles, connections,
objects, forms, and ancestors with CSP. The terminal grant is single-use,
short-lived, bound to one session/workspace/run and carried as a WebSocket
subprotocol rather than a URL. Remote bind, hosted access, browser credential
storage, direct SQLite access, and unbounded event/log/source streaming are not
part of the current contract.

### `database.status`

Takes no parameters. It reports:

- the compiled current-baseline SHA-256 and actual installed `sqlite_schema`
  SHA-256;
- SQLite journal mode (`wal` is required);
- whether foreign-key enforcement is active;
- global physical/canonical SQLite integrity from `PRAGMA quick_check(1)` on a
  short-lived connection without FTS5 registration. It checks page allocation,
  freelist state, and ordinary B-trees without invoking the disposable virtual
  table's semantic integrity hook. Retrieval projection health remains available
  through `knowledge.index.status` and `doctor --retrieval`.

The CLI exposes this as `crewfold doctor --database --socket <path>`.
There is no latest-version, migration, upgrade, or old-baseline negotiation
field. A nonempty mismatch is refused before this method can be served.

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
`expected_revision`, and `idempotency_key`. `agent.show` requires an exact ID or
name selector; `agent.list` returns definitions ordered by name and ID.

### Objectives

`objective.create` takes `workspace`, `project`, `title`, an explicit `budget`
object, and `idempotency_key`. A budget has non-negative `token_limit`,
`cost_cents`, and `time_seconds`; zero means unlimited/not enforced for that
dimension. `objective.update` atomically replaces supplied title, status, or
budget fields and requires `expected_revision`. `objective.show` requires an
exact objective ID; `objective.list` scopes results to a project.

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
task with no open scheduling intent. A partial unique index and transaction
invariant allow only one active primary assignment per task. It creates a durable
assignment record and changes the task to `assigned`; it does not start a runtime.
Manual assignment cannot race or replace accepted manager work in
`pending`, `deferred`, `awaiting_approval`, or `run_requested` intent state.

`task.transition` currently accepts `start`, `block`, `unblock`, or `cancel`:

- `start` changes `assigned` to `active` as coordination state only;
- `block` requires a reason and accepts ready, assigned, or active tasks;
- `unblock` returns to assigned while a lease remains active, otherwise ready;
- `cancel` releases an active assignment but retains its history. It cannot split
  a reserved requested/starting/active run from its assignment. In the same
  transaction it closes a pending/deferred scheduling intent, or a
  `run_requested` intent whose exact latest retry-chain run is a definite
  `start_failed`, and appends one `supervisor.intent_cancelled` fact. A later
  supervisor pass cannot retry that owner-cancelled intent.

`task.show` requires an exact task ID. It and `task.list` return task details,
dependency edges, any active assignment, and derived readiness. `ready_only`
filters deterministically by
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
bounded reverse dependents, authorized participant-thread snapshots, policy,
reporting instructions, the source event high-water, live policy, and explicit
included/excluded explanations.
`context.show` returns that exact packet; `context.explain` returns its stable
selection reasons, semantic hash, and byte size.

Build and show return the single current context-packet/result schema. Stored
packets are immutable and never rewritten in place.

`context.refresh` is the owner-only live mutation. Its strict params are
`workspace`, `run`, and `idempotency_key`; it accepts no caller cursor, task,
agent, or packet. It returns
`urn:crewfold:schema:local-api:context-refresh-result:v1`, type
`context_refresh`, and status `created|pending|up_to_date|rebase_required` plus
the exact run/base packet, durable state revision, inspected event interval,
chain state, optional immutable delta, optional rebase reason, and
`event_sequence`. The event sequence identifies the built/rebase fact and is zero
for pending and up-to-date results. A
matching key replays. Any key while one delta is pending returns that same object
without scanning. A no-change refresh durably advances the inspected cursor but
emits no empty delta or event.

`context.delta.list` takes `workspace`, `run`, `after_sequence` (zero or greater),
and `limit` from 1 through 100. It returns
`urn:crewfold:schema:local-api:context-delta-list-result:v1`, type
`context_delta_list`, with the current durable chain state and a run-local
sequence page. `context.delta.show` and `context.delta.explain` each take
`workspace` and exact `delta`; their result types are `context_delta` and
`context_delta_explanation`. These are owner inspection queries and do not fetch
or acknowledge for the run. No owner/local acknowledgement method exists.

Each delta is immutable, based on the current packet, and capped at 16 KiB; the chain is
capped at 64 KiB and one refresh examines at most 1,000 potentially applicable
events. Stable failures are `invalid_context_delta` and
`context_delta_not_found`; rebase crosses refresh/fetch as a typed result rather
than an error. It requires a replacement run and cannot be bypassed with a
supplied cursor.

`run.start` takes `workspace`, task ID, optional checkout ID, optional context
packet ID, runtime, provider, one validated deterministic scenario,
`expected_task_revision`, and
`idempotency_key`. It
requires the task to be `assigned` with a current lease. The assigned agent must
be enabled, below `max_concurrency`, and configured for the exact opaque
runtime/provider pair. Admission applies the current workspace limits to manual
and supervised starts in the same transaction as run creation. A saturated
workspace/project/provider/node returns retryable
`execution_capacity_exhausted`, names the exact limiting dimension and counts,
and appends no event.

Placement is project-scoped and source-layout neutral. An eligible checkout is
available, writable, and has write-policy capacity. `shared` allows concurrent
runs; `exclusive` and `claimed` reject another live run. With no explicit
checkout, selection is deterministic by write-policy preference, normalized path,
and stable ID. The committed run contains the chosen task, agent, checkout path,
write mode, adapter pair, and human-readable reasons. Creating a run and its
pending worker job, context binding, and expiring capability is one transaction;
no adapter call occurs in that transaction. Without an explicit packet, the same
transaction creates one. An explicit packet must match current task, agent, and
checkout revisions and cannot be reused by another run. A prebuilt current packet also
undergoes one bind-time canonical revalidation: its frozen run authority must
still match, and every embedded knowledge revision must remain accepted, current,
fresh, applicable, and undisputed. Failure requires a new packet without changing
the old bytes. After successful binding, reads preserve the historical base and
later drift is exposed only through explicit context refresh or rebase.

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

`run.show` requires `workspace` and an exact run ID. `run.list` accepts optional
task and status filters. Both return run, current task/agent/checkout projections,
the run timeline, and an accepted handoff when present. Public records and event
payloads never contain runtime/provider handles; list rows expose only derived
`can_attach`. `run.resume` requires the
observed run revision and resumes a blocked run or an explicitly paused active run
from its persisted cursor. `task.timeline` returns the task, all its runs, and
ordered normalized run/timeline facts.

`run.logs` takes `workspace`, run ID, and a line-tail bound. It returns runtime
state plus independently capped stdout/stderr, captured/omitted byte counts, and
truncation flags. Secret-like values are redacted before persistence. A live run
uses its node-bound binding; a terminal run uses immutable content-addressed
artifacts capped at 64 KiB per stream. A terminal/lost run without trustworthy
retained output returns `run_logs_unavailable`, never an empty successful capture.

`run.stop` takes `workspace`, run ID, `expected_revision`, a grace period from 1
to 30,000 milliseconds, and `idempotency_key`. It moves an active/blocked run to
`stopping`; the worker requests graceful process-group termination and then a
forced fallback. The terminal `stopped` record preserves whether force was used,
returns the task to `assigned`, and retains its lease. If process identity cannot
be trusted, the run becomes `lost`, the task remains blocked, and capacity is not
released automatically.

`run.lost.resolve` is the owner-only resolution for that uncertainty. It takes
`workspace`, exact `run`, `expected_revision`, a 1–2,048-byte `note`, literal
`runtime_retired_confirmed: true`, and `idempotency_key`. The owner must first
retire the external runtime through its native control surface. Crewfold performs
no external stop. One successful mutation changes the run to failed with
`runtime_retired_by_owner`, records captured/unavailable log state, clears the
node binding, releases capacity, leaves the task blocked for an explicit retry or
reassignment, and appends `run.lost_resolved` exactly once.

For the manager proposal surface, a post-resolution blocked task with no reserved
run or open scheduling intent is an actionable `reassign_task` target. The
request freezes the exact task revision and one authorized launch profile; owner
approval readies the task and creates a new scheduling intent. `retry_task`
remains limited to the retained assignment of a definite `start_failed` run.

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

`inbox.list` takes canonical workspace and agent IDs plus a limit from 1 through
50. The Go client and CLI preserve friendly name selection by resolving it first
through the exact `workspace.show` and `agent.show` reads; the inbox wire request
itself is therefore unambiguous even when it is empty. It is an owner inspection
query and does not advance delivery state. `thread.show` takes
`workspace` and thread ID and returns its ordered messages and delivery records,
including acknowledgement and separate wake status/diagnostic.

`thread.list` is the bounded discovery read for this history. It takes canonical
`workspace`, optional canonical `project`, and a limit from 1 through 50. Each
newest-first summary carries the thread, exact message count, and the bounded set
of durable agent participants observed as senders, recipients, or frozen
participant bindings. The web Domain Home uses the project scope; an agent view
filters the same canonical result by participant ID. Listing or opening a thread
does not acknowledge messages or create a domain event.

`thread.create` creates the explicit cross-project collaboration exception. It
takes `workspace`, a 1–160 byte UTF-8 `subject`, two through eight bindings with
unique agents and unique tasks spanning at least two projects, and an idempotency
key.
Every agent must be enabled and actively assigned to the exact task at creation.
It returns a `participant_bound` collaboration whose participant revision starts
at one. The result wrapper has type `participant_thread_mutation` and carries the
complete `collaboration` plus its `event_sequence`. Runs cannot call this
owner-socket operation.

`thread.invite` takes `workspace`, thread ID, one new `{agent,task}` binding, the
exact positive `expected_participant_revision`, and an idempotency key. A success
adds one immutable binding and advances the roster revision; a stale revision
returns `revision_conflict` without changing the thread. The total remains capped
at eight. `thread.participants.list` is a read-only owner query returning the
ordered frozen bindings and current participant revision in a
`participant_thread` result wrapper. Both mutations return stable
`invalid_message`, `message_denied`, or `revision_conflict` codes as applicable.

Participant messages still target one agent. Their origin comes from an
authenticated run's exact task/project binding, and artifacts are rejected in
this first slice. An owner send into a participant thread must omit project and
task, producing a null origin; explicit owner scope is rejected. Direct
`message.send` behavior remains project-scoped and keeps its existing sender-owned
artifact rules.

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

`overlap.list` filters by project and `open|resolved`; `overlap.inspect` requires
only `workspace` and an exact overlap ID, while `overlap.scan` has a distinct
closed contract containing `workspace` and an optional project. Inspection returns
the two claim/task IDs, witness, severity, effective response, scheduling flags,
and deterministic explanation. Scanning asks the daemon watcher to inspect
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

### Deterministic curator

`curator.queue` takes `workspace`, `project`, an optional opaque `after` cursor,
and an optional limit defaulting to 50 and capped at 200. It projects current
proposed revisions in ascending `(proposed_at,id)` order. Each complete revision
is classified `manual_review` with a stable reason or `safe_auto_accept` only when
an intact immutable derivation exactly matches
`accepted_meeting_resolution_copy/v1` and the effective rule is enabled. The
result contains the complete effective rule snapshot, including its optimistic
revision; each entry repeats its evaluated enabled state and includes any
derivation. Rule and entries are evaluated in one read transaction. The queue is
read-only and appends no event.

`curator.rule.configure` is the owner-only enable/disable mutation. It takes
`workspace`, exactly `accepted_meeting_resolution_copy/v1`, a required Boolean
`enabled`, the exact observed `expected_revision`, and an idempotency key. Every
workspace has a persisted disabled revision-one rule. Configuration appends an
immutable revision and `curator.rule_configured`; stale revision and idempotency
conflicts change nothing.

`curator.process` takes `workspace`, `project`, optional `apply_safe` (false by
default), and an idempotency key. One transaction observes at most 100 candidates,
and, only when `apply_safe` is true and the exact rule is enabled, accepts at most
ten safe entries. Already-derived safe entries are evaluated first. The remaining
candidate budget prioritizes valid exact-copy sources, creates their missing
derivations, and may accept them in the same pass while acceptance capacity
remains; invalid-source skip evaluations follow. This ordering prevents either
repeatable invalid sources or unrelated manual proposals from starving safe work.
A false or disabled pass may derive but never accepts.
Retries replay the same process result only for the same command/state contract;
partial derivation, authority, and event changes roll back together.

The only transform creates a project-wide `decision`: title exactly the concluded
meeting agenda, body exactly its accepted proposal summary, `medium` confidence,
`supported` verification, `until_superseded` freshness, and exactly one primary
`meeting_proposal` source at the accepted revision. Agenda/body are valid UTF-8
1–160/1–2048 bytes and are never truncated. The auto-accept operation revalidates
rule, source, output hash, derivation, and proposal state, then records
`subsystem:curator` / `allowed` / `state_policy`, the ordinary
`knowledge.accepted` fact, and `curator.auto_accepted`. These methods expose no
actor, content, source, task-scope, or predecessor override.
Out-of-bounds structured source text is returned in a bounded `skipped` list with
exact source identity and a stable reason. It creates no knowledge revision,
derivation, queue entry, authority record, or event; a later process evaluation may
report it again. An accepted proposal summary above 2 KiB reports
`summary_not_exact_safe_copy`.

### Exact knowledge contradictions

`contradiction.report` takes `workspace`, two distinct exact revision IDs,
`reason` (1–2048 encoded UTF-8 bytes), and an idempotency key. It canonicalizes
revision order. Both revisions must be accepted/current, be different items in
one project, and have intersecting project-wide/task applicability. It creates a
`proposed` record and does not change knowledge currency or retrieval.

`contradiction.show` takes `workspace` and the exact `kcon_...` ID. It returns the
record, both complete exact revision snapshots, `authority_check_count`, and only
the newest at most 200 authority checks ordered by event sequence then ID
descending. `contradiction.list` additionally requires `project`; optional
`status`, exact participant `revision`, and limit filters are strict. An omitted
status returns active `proposed|open` records, newest first by reported time then
ID. The default is 50, maximum is 200, and v1 has no cursor. Each list item is the
same coherent bounded detail shape.

`contradiction.confirm` and `contradiction.dismiss` take `workspace`,
`contradiction`, `expected_state_revision`, an optional note bounded to 2048
encoded bytes, and an idempotency key. Caller payloads cannot select an actor.
Only the local owner can confirm a still-eligible proposed record `open`; the
owner may dismiss a proposed or open record. Allowed authority evidence and the
lifecycle event commit in the same transaction. A stale/superseded participant
automatically resolves every incident open record with exact governing-event
linkage.

`knowledge.dispute` takes `workspace` and an exact revision. It derives, without
mutation, whether any open record references the revision, the total open count,
and the first at most 200 contradiction IDs in ascending lexical order. Open
participants are quarantined everywhere they otherwise apply. Search filters them
before ranking/limit, while a new explicit context build returns
`knowledge_conflict` for an otherwise eligible disputed pin and commits nothing.
Existing current-packet bytes remain unchanged. See
[ADR-0012](../decisions/0012-owner-confirmed-exact-knowledge-contradictions.md).

### Portable project knowledge

`knowledge.export` requires `workspace`, `project`, and an absolute clean
daemon-local `directory`. It obtains one coherent read snapshot and returns the
canonical manifest and Markdown bytes to the daemon handler, which writes that new
private destination directory. The
`urn:crewfold:schema:local-api:knowledge-export-result:v1` result type is
`knowledge_export`: directory, `kbun_...` ID, full content digest, flat manifest
and Markdown byte-size/SHA-256 fields, record counts, and a result-only
`as_of_event_sequence`. Export is read-only, is independent of FTS, and appends no
event.

`knowledge.import` requires the exact target `workspace`, `project`, full expected
content digest, caller-generated idempotency key, an absolute clean daemon-local
bundle `directory`, and an explicit `create_scope` Boolean. The CLI's optional
`--create-scope` flag maps omission to `false`; the API field itself is required.
Bundle bytes are not carried in the bounded request. Request payloads cannot
select an actor. The handler injects the local-owner actor; no MCP/run method
exists.

Import rejects unknown manifest fields, noncanonical JSON or Markdown,
digest/count/scope/lifecycle inconsistency, unsafe bounds, missing task anchors,
and nonempty or colliding canonical target scope. With `create_scope`, it may
create only the exact missing workspace/project and portable task anchors. It
never creates operational tasks, meetings, agents, runs, repositories, checkouts,
or capabilities. The
`urn:crewfold:schema:local-api:knowledge-import-result:v1` result type is
`knowledge_import`: bundle and digest metadata, immutable receipt,
`imported|already_present` status, created-scope counts, and local event
high-water. The same exact completed bundle
replays under the same or another command key without a second event.

Stable failures distinguish `knowledge_export_path_exists`,
`invalid_knowledge_bundle_path`, `invalid_knowledge_bundle`,
`knowledge_bundle_digest_mismatch`, `knowledge_import_scope_conflict`, and
`knowledge_import_conflict`. Reusing one idempotency key with a different
normalized request returns the ordinary `idempotency_conflict`; an unexpected
durable I/O/database failure remains `storage_failed`. Every rejected import is
zero-write. The store also retains `knowledge_import_denied` as defense in depth
if a non-owner actor ever bypasses the owner-only surface.

One new import appends `knowledge.imported` for each revision,
`contradiction.imported` for each contradiction, then
`knowledge.import_completed`. These local-owner attestations authorize the
imported final state. Origin event and authority ledgers, curator proof rows, and
idempotency records are not bundle contents and are not replayed. See
[ADR-0013](../decisions/0013-portable-project-knowledge-snapshots.md).

### Manager grants and launch profiles

`manager.grant.create` is owner-only and requires an exact workspace, project,
objective, planning task and task revision, agent and agent revision, one through
four proposal kinds, one through 32 pre-existing target launch-profile IDs, zero
through three claim kinds, bounded proposal limits, an optional expiry, and an
idempotency key. The proposal kinds are `task_decomposition`, `assignment`,
`review`, and `escalation`; claim kinds are `path`, `component`, and `operation`.
It returns `manager_grant_mutation` with the full immutable grant and event
sequence. `manager.grant.revoke` requires the exact current revision and reason.
Show/list return `manager_grant`/`manager_grant_list` and can filter by exact
scope, agent, status, and a limit no greater than 100.

The grant is the authority. An agent definition's `role` remains any
owner-selected descriptive string; neither the role, agent name, nor launch
profile `purpose` can authorize a manager call. A manager run must still match the
grant's exact active assignment, task/agent revisions, planning launch profile,
current-packet management-grant snapshot, capability, expiry, proposal kind, and target-profile set at
every proposal call. Revocation stops later proposal calls from an already-live
run.

`launch_profile.create` requires an exact workspace/project/agent revision,
runtime, provider, bounded fake/direct fixture scenario, optional checkout,
assignment lease and capability lifetime from 30 through 86,400 seconds, and an
idempotency key. An optional `manager_grant` creates the planning profile for that
grant; target profiles omit it and must exist before grant creation.
`launch_profile.retire` requires an expected revision and reason. Show/list return
`launch_profile`/`launch_profile_list` and can filter by project, agent, grant,
status, and bounded limit. Profiles are exact scheduling eligibility, not ranked
candidates; retirement never rewrites their execution content.

### Manager invocation and proposals

`manager.invoke` takes workspace and objective plus either a fully explicit
planning tuple (`planning_task`, `grant`, `profile`, and their expected revisions)
or an unambiguous resolvable tuple, and an idempotency key. It atomically creates
the current packet with an exact management grant, the run and pending job, and context/capability bindings for the
existing exact active assignment. It returns `manager_invocation`: the exact grant
and profile, complete run detail, and final event sequence. Ambiguity, an inactive
grant/profile, stale revision or assignment, scope mismatch, or a planning profile
not bound to the grant fails before any runtime launch.

The run can submit only the proposal kinds frozen in its current-packet grant. One action array
is bounded to 32 actions and 49,152 encoded bytes. `proposal.list`
filters by workspace plus optional project, objective, source run, grant, kind,
status, and limit. `proposal.inspect` returns the complete immutable action array
and validation issues. `proposal.accept` and `proposal.reject` require the exact
pending revision, a bounded decision note, and an idempotency key. They return
`manager_proposal_mutation` with the decided proposal, exact typed effects, and
event sequence.

Submission is intentionally inert. Acceptance revalidates all current scope,
revisions, dependency cycles, count/budget envelope, claim allowlist, and exact
launch-profile bindings in one immediate transaction. It either commits every
task/dependency/claim requirement/scheduling intent/effect/event/idempotency row,
or none. Rejection creates no work effect. The run has no local-API access and no
MCP acceptance tool; `crewfold_accept_manager_proposal` is a recognized denied
probe.

A pending immutable proposal remains owner-decidable after its source planning
run completes and releases the planning assignment. Acceptance still requires
the active unexpired grant, immutable source packet/grant tuple, current source
agent at its frozen revision, exact active objective revision, and current target
references; it does not require or restore a live planning capability.

### Supervisor policy, actions, and approvals

`supervisor.policy.show` returns the effective immutable policy revision.
`supervisor.policy.configure` appends a new owner revision with `enabled`, global
active/starting limits, default and exact project/provider concurrency maps,
`auto_schedule`, retry limit `0..3`, cooldown `0..86400`, optional expected
revision, and idempotency key. A zero manager budget means unlimited, but all
supervisor concurrency limits are positive and bounded; policy configuration does
not itself schedule work.

`supervisor.run` takes workspace, a limit no greater than 100, and an idempotency
key. It returns `supervisor_run` with the exact policy used, newly recorded
actions, scheduled run IDs, and final event sequence. The pass first reconciles
existing requested/starting/active/blocked/stopping/lost runs and expired leases,
then evaluates stable candidates. It counts global, project, provider, and agent
capacity, including lost or reserved work, and creates at most one canonical
condition action. Repeating the same command or racing passes cannot create a
second run for one accepted intent.

Before inspecting projections, each pass captures one workspace-journal cutoff
and classifies closed-union event pages of at most 1,000 facts. A fully understood
partial page advances only the durable cursor; the pass remains effect-free until
it has caught up to that captured cutoff. An event type unknown to the running
binary returns `unsupported_supervisor_event` and leaves the cursor, actions,
approvals, intents, runs, and idempotency state unchanged. This makes adding a new
journal fact an explicit supervisor-compatibility change rather than permission
to skip evidence.

The owner-facing method durably records even a successful no-op result, so replay
of that exact idempotency key can never acquire effects that became eligible
later. Background daemon passes use one-shot keys; an idle pass advances a
classified cursor when needed but emits neither a scan event nor a no-op receipt.
A later fresh pass may act only after it has classified every fact through its
own captured cutoff.

Eligible intents are ordered by task priority descending, readiness time
ascending, then task ID. Readiness time is the latest of intent creation, an
exact task-readied or assignment-expired fact, and completion of every
dependency; an ordinary title, description, priority, or budget update does not
invent a newer ready transition. An unchanged deferral receives a deterministic
30-second `next_attempt_at`. Before that deadline, only a newly classified fact
relevant to the action's sealed primary failing dimension may wake it. The
per-intent event watermark prevents an old fact from repeatedly bypassing
backoff, and deferred queue heads cannot starve a later eligible intent.

`dependency_ready -> schedule` is the sole automatic scheduling rule when the
effective policy is enabled with `auto_schedule`. A bounded cooled-down retry of
the same start-failed task may also be policy-driven. It creates a new receipted
run through the exact frozen profile while leaving the failed run immutable, and
requires the assignment and every required claim to remain active and unexpired.
The retry action exposes the immutable failed run as optional `prior_run_id` and
the fresh requested run as `run_id`; non-retry actions omit `prior_run_id`.
Conditions `blocked`,
`stale`, `failed`, `repeated_failure`, and `over_budget`, plus stop/resume/reassign
responses, create inspectable action/approval records instead of applying hidden
control. `supervisor.action.list` filters by project/task/run/status/condition;
`supervisor.action.show` returns one frozen action. `supervisor.explain` is a
read-only result containing the exact policy/event cursor and, for each intent,
eligibility, reasons, and constraint snapshot.

Intent acceptance begins at `pending`; capacity or authority contention may move
it to `deferred`, and a committed placement moves it to `run_requested`.
Completion closes it `satisfied`; rejected completion or a definite runtime
failure closes it `failed`; a stopped run closes it `cancelled`. A definite
`start_failed` remains `run_requested` only while the current policy still
authorizes another exact bounded retry. Exhaustion, disabling/lowering retry
policy, or owner cancellation closes it exactly once. Fresh retry runs retain the
original intent and only the latest sealed successor can determine its terminal
result.

The scheduling or retry receipt freezes authority for that already committed run
operation. Worker claim/start revalidates the exact receipt, job, run, current
task, and active assignment linkage, but it does not reinterpret a later profile
retirement, agent disablement or revision change, or passage beyond the assignment's
lease timestamp as revocation of the committed launch. Reserved-run
reconciliation prevents that assignment from being released merely because wall
time advanced. Those changes do block future placements and retries, which must
revalidate current authority and create a new receipt.

An accepted proposal's `request_action` becomes a `manager_escalation` action and
one inert approval request. The supervisor action freezes typed
`source_proposal_id` and `source_action_id` together with the requested response,
exact target/revision, optional reassign profile, and reason; it cannot be
detached from or substituted across proposals. Owner allow revalidates that
closed target before applying the supported response, while deny/replay has no
second effect.

`approval.list` filters by status/action and `approval.inspect` requires an exact
approval ID and returns that request. `approval.allow` and `approval.deny` require
the pending request's expected revision, a bounded decision note, and an
idempotency key. The result type `approval_mutation` returns both approval and
bound supervisor action plus event sequence. One action has at most one approval;
a stale or repeated decision returns a conflict rather than applying twice. An
allow authorizes only that closed action at its frozen revision and never becomes
a reusable grant.

Stable M16 failures are:

```text
invalid_manager_grant
manager_grant_not_found
manager_grant_denied
invalid_launch_profile
launch_profile_not_found
invalid_manager_proposal
manager_proposal_not_found
manager_proposal_conflict
manager_proposal_denied
invalid_supervisor_policy
supervisor_action_not_found
unsupported_supervisor_event
approval_not_found
approval_conflict
```

## M17 local-check methods

The owner-local protocol remains version 1. M17 methods use the existing strict
decoder: unknown or duplicate JSON fields are invalid, no public parameter names
an actor, and every mutation has an idempotency key. Lifecycle and decision
mutations also require the exact current revision.

Definition methods are:

```text
check.definition.create
check.definition.retire
check.definition.show
check.definition.list
```

Create accepts `workspace`, `project`, `name`, `executable`, ordered `arguments`,
`working_directory`, `timeout_millis`, `output_byte_limit`, and
`idempotency_key`. `executable` is absolute; the working directory is normalized
relative to the chosen checkout. There is no command-string, shell, stdin,
environment, credential, provider, MCP, role, or purpose field. Retire accepts
the definition ID, `expected_revision`, bounded reason, and idempotency key.

Named task acceptance criteria use:

```text
check.requirement.create
check.requirement.retire
check.requirement.list
```

Create accepts the workspace, task, `criterion_key`, criterion statement, exact
definition ID/revision, expected task revision, and idempotency key. Retire
requires its expected revision. A list result always includes derived
`missing|running|verified|failed|stale|unknown` state; missing and stale rows are
not omitted.

Delegation and deterministic routing use:

```text
check.grant.create
check.grant.revoke
check.grant.show
check.grant.list
check.route.create
check.route.retire
check.route.list
check.policy.show
check.policy.configure
```

A grant names one project, exact agent revision, exact definition revisions, a
subset of `run|inspect|propose_repair`, pending/in-flight limits, optional expiry,
and idempotency key. It never accepts or resolves an agent role or launch-profile
purpose. A route names an optional exact definition, trigger
`pass|nonpass|stale`, duty `evidence_review|coordination`, exact agent revision,
and lifecycle fields. The mandatory failure-to-current-task-owner route is
system-defined and not caller-remappable. Policy defaults repair proposals to
disabled; enabling them requires one exact active repair launch-profile revision
and a bounded open-proposal limit.

Execution and inspection use:

```text
check.run
check.list
check.inspect
check.logs
check.watch
```

`check.run` accepts `workspace`, task, definition name or ID, optional checkout,
expected requirement/definition/checkout revisions, and idempotency key. The
server resolves exactly one active requirement. When checkout is absent, it uses
the currently reserved task run's checkout, then the latest task run checkout in
a stable order, and otherwise rejects the request. The response returns the
durable requested check run; it does not wait for or predict its result.

`check.inspect` requires an exact check-run ID and returns the frozen run,
definition and requirement revisions,
launch receipt, source observations, one optional terminal result, current
freshness revision and reason, bounded artifact metadata, mechanical evidence,
all four evidence-class buckets, notification/route failures, repair proposal,
and the derived requirement state. Process outcome and freshness are separate.
Only passed plus fresh from the latest exact active requirement revision is
`verified`.

`check.logs` returns only bounded, redacted retained stdout, stderr, and diagnostic
content with captured/omitted byte counts, truncation, and hashes. It cannot expose
raw runtime-inspection captures.

`check.watch` accepts one project, bounded cursor/limit, and idempotency key. It
performs one pass of at most 100 reconciliation, fresh-Git inspection, routing,
and repair-staleness candidates. It does not launch missing checks. A public exact
no-op stores a receipt, emits `check.watch_completed`, and replays the same result
with its nonzero event sequence. The daemon background path uses the same
classifier but writes no receipt/event for an exact no-op. The public completion
event confirms the requested pass committed; clients use the receipt counters,
not the event's presence, to decide whether freshness changed.

Repair handling is owner-only:

```text
check.repair.list
check.repair.inspect
check.repair.accept
check.repair.reject
```

Accept/reject takes the exact proposal, expected revision, bounded decision note,
and idempotency key. Acceptance revalidates the latest exact trusted failed result
at the current fresh source, authenticated watcher run/agent/grant tuple, and
current policy/profile/task/objective before atomically creating one linked repair task
and scheduling intent. A proposal remains inert before that decision; a later
fresh pass makes it stale. List, inspect, and mutation details expose an optional
immutable decision with proposal revision, optional canonical note bounded to
4096 encoded UTF-8 bytes, timestamp, and exact `local-owner` author; pending and
stale-undecided proposals omit it. Only an accepted decision has a repair effect.
Timed-out, start-failed, and unknown outcomes or stale/unknown freshness remain
inspectable but cannot seed a proposal.

Stable M17 failures include:

```text
invalid_check_definition
check_definition_not_found
invalid_check_requirement
check_requirement_not_found
check_requirement_conflict
invalid_check_watch_grant
check_watch_grant_not_found
check_watch_grant_denied
invalid_check_route
invalid_check_policy
check_run_not_found
check_run_conflict
check_runtime_unknown
check_artifact_unavailable
unsupported_check_event
check_repair_not_found
check_repair_conflict
check_repair_denied
```

No check result, watch pass, notification, or repair proposal invokes Git commit,
push, merge, deployment, task completion, policy acceptance, or integration-order
selection.

## M18 outcome and briefing methods

The M18 protocol is one current owner-local surface. Unknown fields are invalid,
mutations require idempotency keys, and no request accepts an actor, role, agent,
MCP capability, evidence class, freshness label, verification strength, or
briefing rendering option.

Commitment methods are:

```text
outcome.commitment.create
outcome.commitment.show
outcome.commitment.list
```

Create accepts `workspace`, exact `task`, owner-visible `key`, `title`, optional
`description`, one to 32 ordered `acceptance_criteria`, and `idempotency_key`.
The task, objective, and project scope are resolved from the exact current task.
The immutable commitment must exist before any assessment that cites it. Show
accepts only `workspace` and `commitment`; list accepts workspace plus bounded
project, objective, task, and limit filters.

Assessment methods are:

```text
outcome.assessment.propose
outcome.assessment.show
outcome.assessment.list
outcome.assessment.accept
outcome.assessment.reject
```

Propose requires `workspace`, exact `task`, exact `commitment`, structured
`assessment`, and `idempotency_key`; `supersedes_outcome` is present only for a
successor to the exact current accepted assessment. The Store rejects a task that
does not match the commitment. Assessment input has a closed conclusion
`achieved|partial|not_achieved|unknown` and required ordered arrays for delivered
scope, unmet scope, exact decision revision IDs, evidence, effects, deviations,
risks, unknowns, follow-up tasks, and owner attention. Caller evidence source type
is closed to `handoff|check_requirement_evidence`. The Store resolves each source
and derives class, freshness, strength, dispute state, and current truth.

Show accepts only workspace and assessment ID. List accepts bounded project,
objective, task, commitment, review-state, conclusion, and limit filters. Accept
and reject require the exact assessment, `expected_state_revision`, optional
bounded `decision_note`, and an idempotency key. A successful successor acceptance
atomically marks the old current assessment `superseded` and the successor
`accepted`. Rejection leaves the current accepted assessment unchanged.

Checkpoint methods are:

```text
checkpoint.create
checkpoint.show
checkpoint.list
```

Create freezes one exact `task|objective|project|workspace` scope, current event
sequence, timestamp, and local-owner identity. Show accepts only workspace and
checkpoint ID. List accepts one exact scope and a bounded limit. Checkpoints are
immutable and have no lifecycle mutation.

Briefing methods are:

```text
briefing.show
briefing.explain
```

Show accepts `workspace`, exact `scope_type`, exact `scope_identifier`, and an
optional same-scope `since_checkpoint`. It always captures the current workspace
high-water; the checkpoint cursor is an exclusive lower bound. There is no
caller-selected historical event cursor. A result contains at most 128 whole
claims and 64 KiB of canonical JSON, plus deterministic omission counts for each
closed section and `claim_limit|byte_limit` reason. Every
claim ID is `bclaim_` followed by 64 lowercase SHA-256 hexadecimal characters and
has exact ordered provenance. Explain accepts workspace, the exact briefing ID,
and one claim ID from that briefing, returning the structured claim, provenance,
and current diagnoses. Neither read appends an event.

Published parameters and result schemas are the closed files under
`protocol/schemas/local/v1/`; reusable domain records are under
`protocol/schemas/domain/v1/`. There is no outcome MCP method, checkpoint archive
method, or alternate briefing representation.

## M19 bounded operator reads

M19 keeps protocol 1 and one current record shape. It directly replaces the
unbounded collection results for agents, objectives, tasks, runs, claims,
overlaps, and drift; there is no old response alias, version-selection flag, or
compatibility list method. It adds the missing owner reads:

```text
workspace.list
project.show
project.list
meeting.list
events.timeline
```

Operator collection, event, inbox, briefing, and intervention wire requests use
canonical workspace/project/agent IDs. `workspace.show`, followed when needed by
`project.show` or `agent.show`, is the only name-resolution path. The Go client
performs those pure reads for CLI name selectors before sending an operator
request, so every returned row and even an empty page can be bound to its exact
scope without trusting an echoed name.

All ordinary collection requests accept `cursor` and `limit`. The default page
contains 50 records and the maximum contains 200. Results contain the typed
record array plus `next_cursor`, `has_more`, and the exact filtered `total`.
Cursors are opaque, at most 256 bytes, keyset-based, and bound to the exact
resolved workspace/project and filters. Reusing one under another scope or filter
returns `invalid_cursor`. A dashboard screen follows no more than three pages, so
one load retains no more than 600 records and reports when the filtered total is
larger.

`project.show` takes a canonical workspace ID and resolves one project ID or name
to its canonical projection only.
Unlike `project.inspect`, it performs no Git observation, checkout refresh, or
event append; it is the project-resolution operation used by the TUI.

`run.list` accepts an optional canonical project filter and returns bounded
`RunSummary` records. Each summary exposes only a derived `can_attach` boolean,
never the runtime handle or attach environment. The full placement, task, agent,
checkout, timeline, and handoff graph remains the one `run.show` result rather
than being duplicated into every list row. Agent role and launch-profile purpose
fields are descriptive output only; no operator query uses either for authority,
urgency, ordering, or action availability.

Operator collection reads are pure current-projection reads. Paging, dashboard
bootstrap, navigation, inspection, and refresh do not reconcile leases or append
events. Time-driven lease reconciliation is performed independently by the
daemon, so a read cannot acquire hidden mutation authority.

### `coordination.status`

Takes `workspace` and returns counts for registered/enabled agents plus
registered, derived-ready, assigned, active, blocked, completed, and cancelled
tasks. The CLI exposes it as `crewfold status --workspace <scope>`; omitting the
workspace retains the process-health form of `status`.

### `events.list`

Requires an exact workspace and returns canonical event envelopes in ascending
sequence order strictly after `after`. The first page atomically captures a
high-water; an opaque continuation binds the workspace, original lower bound,
and that exact cutoff:

```json
{"id":"req-5","protocol":1,"method":"events.list","params":{"workspace":"ws_00000000000000000000000000000001","after":0,"limit":1000}}
```

The default limit is 50 and the maximum is 1000. The result contains the resolved
canonical `workspace_id`, `high_water`, `events`, `next_cursor`, `has_more`, and
the exact filtered `total`.
Every continuation remains at or below the first page's high-water even when new
events commit concurrently. A continuation whose cutoff is ahead of the current
journal fails as a rewind instead of silently resuming against another history.
An envelope invalidates canonical records; clients do not treat its payload as a
second entity projection.

### `events.timeline`

Takes `workspace`, exact bounded `entity_type`, exact `entity_id`, optional
`cursor`, and optional `limit`. It captures a high-water and returns that entity's
events newest-first, with the same ordinary 50/200 page and 256-byte cursor
bounds. The result repeats the resolved canonical `workspace_id`, and every
envelope must match it. It is the canonical detail timeline; the TUI does not
scan or reverse an unbounded workspace journal locally.

The local client rejects a response larger than 16 MiB before JSON decoding.
Malformed, nonmonotonic, wrong-scope, oversized, and rewound event pagination
fails closed. The operator TUI polls at 500 milliseconds with one request in
flight and consumes at most ten 1,000-event pages before yielding.

## M20 health and backup creation

M20 retains one current protocol and adds no old-record or backup-version
negotiation. Only online work is exposed through the daemon. Offline bundle
verification/restoration/activation, repair inspection, and personal load never
receive a socket method. The bundled client gives both online maintenance calls
the frozen 60-second maintenance window; a shorter caller context or explicit
client timeout still cancels an in-flight request.

### `system.doctor.full`

Takes exactly `{}` and is read-only. Its result schema is
`urn:crewfold:schema:local-api:full-doctor-result:v1`, type `full_doctor`, with:

```json
{
  "schema": "urn:crewfold:schema:local-api:full-doctor-result:v1",
  "type": "full_doctor",
  "status": "ok|degraded|failed",
  "event_sequence": 0,
  "baseline": {
    "sha256": "<64-lower-hex>",
    "installed_schema_sha256": "<64-lower-hex>"
  },
  "resources": {
    "database_bytes": 0,
    "referenced_artifact_bytes": 0,
    "rss_bytes": 0,
    "goroutines": 0,
    "open_fds": 0,
    "filesystem_free_bytes": 0
  },
  "limits": {
    "briefing_claims": 128,
    "briefing_bytes": 65536,
    "node_unresolved_runs": 20
  },
  "checks": [{
    "code": "<stable-code>",
    "status": "ok|warning|failed",
    "checked_count": 0,
    "issue_count": 0,
    "summary": "<bounded-text>",
    "samples": [{
      "entity_type": "<type>",
      "entity_id": "<id>",
      "code": "<stable-code>",
      "detail": "<bounded-redacted-text>"
    }],
    "remediation": {"kind": "<stable-kind>", "command": ["crewfold", "..."]}
  }]
}
```

Checks are emitted in this fixed order: `current_baseline`,
`sqlite_integrity_check`, `foreign_keys`, `canonical_integrity`, `event_contract`,
`projection_receipt_parity`, `artifact_integrity`,
`derived_knowledge_index`, `runtime_bindings`, `durable_queues`,
`filesystem_permissions`, `resource_budget`, and `restore_activation`. Every
registered row is checked; only output samples are capped, at 20 per check. The
complete result is capped at 1 MiB and appends no event.

### `backup.create`

Params are exactly:

```json
{
  "target_path": "/canonical/absolute/nonexistent-bundle",
  "idempotency_key": "<1..128 bytes>"
}
```

The result schema is
`urn:crewfold:schema:local-api:backup-create-result:v1`, type `backup`:

```json
{
  "schema": "urn:crewfold:schema:local-api:backup-create-result:v1",
  "type": "backup",
  "backup": {
    "id": "backup_<32-lower-hex>",
    "path": "/canonical/absolute/bundle",
    "created_at": "<canonical-timestamp>",
    "baseline_sha256": "<64-lower-hex>",
    "event_sequence": 0,
    "logical_state_sha256": "<64-lower-hex>",
    "database_sha256": "<64-lower-hex>",
    "manifest_sha256": "<64-lower-hex>",
    "artifact_count": 0,
    "total_bytes": 1048576
  }
}
```

The daemon snapshots first, then evaluates full integrity and quiescence against
that snapshot and copies exactly its referenced immutable artifacts. It publishes
through an absent-or-complete rename. The same normalized target/request/key
replays one result after a lost response; reusing the key for another request is
`idempotency_conflict`. The target must be outside the source data directory and
must not use Crewfold's reserved recovery parent-lock name. Neither the selected
source nor target may be at or below a component matching the reserved recovery
staging grammar: an invalid source fails as `backup_source_unhealthy`, while an
invalid target fails as `backup_target_invalid`, both before receipt, staging, or
source/parent mutation. Component siblings with a shared string prefix remain
valid. Create appends no event.

### M20 admission and stable errors

The current supervisor limits govern every start even when automatic supervision
is disabled. Defaults are eight unresolved workspace runs, two
requested/starting, and four unresolved per project/provider. A fixed node limit
is 20. `requested|starting|active|blocked|stopping|lost` consume unresolved
capacity; `requested|starting` consume starting capacity. Check execution remains
one process at a time. Agent role and launch-profile purpose never affect these
counts or authority.

M20 adds these stable failures; no deprecated aliases are accepted:

| Code | Retryable | Meaning |
| --- | --- | --- |
| `current_baseline_mismatch` | no | nonempty database is not the one compiled current baseline |
| `canonical_integrity_failed` | no | current-baseline canonical/durable state failed a full invariant |
| `database_busy` | yes | bounded SQLite write contention; no generic storage remap |
| `backup_source_unhealthy` | no | captured source failed a required health gate |
| `backup_not_quiescent` | yes | captured cut contains actionable work/binding/queue state |
| `backup_target_invalid` | no | target path or parent is unsafe/invalid |
| `backup_target_exists` | no | create target already exists |
| `backup_contract_mismatch` | no | manifest/baseline is not the one current contract |
| `backup_integrity_failed` | no | manifest, bytes, SQLite, logical state, or artifacts disagree |
| `restore_target_exists` | no | restore never overwrites or merges |
| `restore_not_activated` | no | restored data directory is still pending/inert |
| `restore_unsafe_nonterminal` | no | activation/first-start state contains live work or binding |
| `restore_source_retirement_unconfirmed` | no | activation lacks the explicit disaster-recovery assertion |
| `repair_source_in_use` | yes | a live daemon owns the inspected data directory |
| `repair_target_invalid` | no | selected offline data directory is unsafe/invalid |
| `execution_capacity_exhausted` | yes | exact workspace/project/provider/node admission limit reached |
| `runtime_binding_unavailable` | no | control requires a live binding on this node |
| `run_logs_unavailable` | no | no trustworthy live or immutable terminal capture exists |
| `resource_limit_exceeded` | no | a documented manifest/report/load safety bound was exceeded |
| `operation_cancelled` | yes | cancellation occurred before publication/commit |

## M22 domains and durable agent sessions

M22 presents an existing canonical Project as a domain and an Objective as a
workstream. These are current presentation meanings, not compatibility aliases
and not new authority containers. A repository or checkout is an attached
resource; it is not the domain's identity. Agent names such as `domain-steward`,
`lead`, `reviewer`, or `builder` are ordinary owner-authored names and descriptive
roles. None is reserved, required, or authority-bearing.

The strict domain-agent methods are:

| Method | Effect |
| --- | --- |
| `domain.agent.spec.draft` | run one read-only ephemeral Codex helper and return an uncommitted owner-reviewable name/role/charter/policy draft |
| `domain.agent.create` | owner atomically creates one agent definition and its membership in the selected domain |
| `domain.agent.attach` | attach an existing workspace agent definition to the selected domain |
| `domain.agent.update` | revise parent, workstream, preferred entry, or active/retired state with optimistic revision checking |
| `domain.agent.tree` | return the flat, canonically ordered definition+membership set from which clients render the proven-acyclic hierarchy |
| `domain.agent.session.open` | bind or resume the selected agent's private Codex provider thread in one attached checkout |
| `domain.agent.session.show` | read bounded owner/agent turns and structured activity without exposing provider thread or node identifiers |
| `domain.agent.session.send` | send one exact owner message to that durable agent's provider conversation |
| `domain.agent.session.interrupt` | interrupt the exact current turn; it does not retire the agent or discard the durable conversation |
| `domain.agent.staffing_grant.create` | owner grants one agent bounded authority to create continuing durable descendants |
| `domain.agent.staffing_grant.list` | list the manager's exact active, revoked, and derived-expired grants |
| `domain.agent.staffing_grant.revoke` | revoke one exact current grant revision |
| `domain.work_proposal.list` | list bounded coordinator-authored pending and historical workstream graphs for owner review |
| `domain.work_proposal.accept` | owner accepts one exact pending revision and atomically creates its workstream, tasks, dependencies, and scheduling intents |
| `domain.work_proposal.reject` | owner rejects one exact pending revision without creating graph state |

`domain.agent.spec.draft` accepts either an exact existing domain/checkout scope
or a pre-onboarding repository path/domain name plus bounded owner intent. It uses
an ephemeral read-only provider thread with no Crewfold tools or effect approval,
returns exactly `name`, `role`, `operating_charter`, `delegation_policy`, and
`rationale`, and appends no event. It is assistance, not creation.

`domain.agent.create` accepts exact workspace/project/name/role/provider/runtime,
`max_concurrency`, required owner-reviewed `operating_charter` and
`delegation_policy`, optional parent/workstream/preferred-entry, and an
idempotency key. Its result contains the joined agent plus the two committed event
sequences. Creation does not open a provider session, create a task, launch a run,
grant staffing authority, or infer authority from the chosen name, parent, role,
charter, or policy. Attach and child creation require the same charter/policy;
update may revise them with the exact expected membership revision.

A staffing grant freezes: the manager membership revision; allowed
provider/runtime/max-concurrency profiles; task-class slugs; descendant and total
concurrency ceilings; cumulative token/cost/time allocation; optional expiry; and
its own revision. Child creation is a provider-originated structured tool effect,
not prose interpretation. Grant scope, current manager membership, expiry,
profile, task class, descendant count, concurrency, budget, domain, and
idempotency are checked together in the child-creation transaction. Revocation
racing a child request therefore yields either one fully recorded child under the
still-current grant or a denial with no partial agent/membership/allocation.
Budget dimensions use the existing Crewfold convention: zero is unlimited. An
unlimited requested child dimension is denied beneath a finite grant; a finite
grant otherwise accounts exact cumulative child allocations. Human-facing
clients should explain common task classes rather than require owners to memorize
slugs, while still submitting the exact selected or custom class.

Setting a domain membership to `retired` is a history-preserving lifecycle
transition, not deletion. It is refused while that membership owns active child
memberships, nonterminal assigned tasks, unresolved runs, or active staffing
grants. Setting an objective-backed workstream to `cancelled` is the existing
objective status mutation: it preserves references and never cascades to agents
or tasks. The owner web lifecycle review withholds cancellation while it
observes active scoped agents or nonterminal tasks so normal organization does
not hide live work. Neither mutation silently reparents agents, revokes grants,
cancels tasks, or deletes journal history.

Every opened durable Codex thread is advertised the following closed Crewfold
dynamic-tool set:

- `crewfold_get_domain_context`: bounded domain, hierarchy, attached resources,
  workstreams, assignment, delivered inbox, and current staffing grants;
- `crewfold_send_message`: one immutable typed message to another active durable
  agent in the same domain; and
- `crewfold_create_durable_child`: one typed child request under an explicit
  current staffing grant;
- `crewfold_propose_work`: one bounded inert objective/task/dependency graph under
  the coordinator's current staffing grant; and
- `crewfold_propose_knowledge`: one sourced proposed domain-knowledge revision
  whose governance state remains pending until an owner decision.

A work proposal freezes its source agent/thread, staffing-grant revision,
canonical event cut, content hash, objective budget, task keys/classes/budgets,
exact launch profiles, and dependency keys. Accept and reject require its exact
current revision plus an idempotency key. Acceptance returns every applied
objective/task/dependency/scheduling-intent effect and its event sequence. If
the grant, membership, launch profile, or graph is no longer current, the
proposal becomes `stale`; the daemon never partially applies it.

Message delivery to a durable session never races an owner turn. A wake targeting
a thread with an active provider turn is returned to pending with a bounded
retry time. Once the thread is idle, the message is delivered through a distinct
provider turn whose input provenance is `crewfold_delivery`.

The durable provider conversation is resumed with a read-only checkout sandbox.
It is a real Codex thread for inspection, planning, communication, and these
audited structured operations, but it is not a Crewfold implementation run.
Owner prose cannot authorize repository writes. Implementation, review, and
verification source effects require an exact assigned Crewfold run. Creating a
durable child records only its definition and hierarchy membership; it does not
assign a task, reserve a checkout, or start that child.

Each tool exchange has a durable, replay-safe receipt. Tool and session results
exclude the provider thread ID, node identity/fingerprint, capability material,
private reasoning, and raw transcript. Provider activity is bounded display data;
accepted messages, hierarchy, grants, tasks, evidence, and knowledge remain
canonical Crewfold state.

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
  durable; live clients use bounded polling rather than a parallel streaming
  protocol.
- Unix sockets are the only supported transport; Windows named pipes are later.
- Socket permission is a transport boundary, not future agent authorization.
