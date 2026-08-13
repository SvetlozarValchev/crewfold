# Local API v1

Status: implemented for daemon health, durable workspaces/events, read-only Git
inspection, provider-neutral agent/objective/task/run coordination, immutable
context packets, owner-facing durable agent mail, leased claims with deterministic
overlap/drift inspection, structured overlap-resolution meetings, canonical
knowledge, and deterministic derived retrieval. Subscriptions arrive later.
The owner-local surface also exposes the bounded deterministic curator queue,
rule configuration, explicit processing pass, and exact knowledge-contradiction
governance, portable project knowledge snapshots, and explicit bounded live
context refresh and inspection. M16 adds owner-granted manager invocation and
proposal decisions, exact launch profiles, deterministic supervision, and an
owner approval queue.

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
bounded reverse dependents, authorized participant-thread snapshots, policy,
reporting instructions, the source event high-water, live policy, and explicit
included/excluded explanations.
`context.show` returns that exact packet; `context.explain` returns its stable
selection reasons, semantic hash, and byte size.

New builds return context-packet/result schema v4. `context.show` can carry a
preserved v1, v2, or v3 packet; it never upgrades the stored packet in place. Old
packets do not advertise live MCP tools and require rebase for refresh.

`context.refresh` is the owner-only live mutation. Its strict params are
`workspace`, `run`, and `idempotency_key`; it accepts no caller cursor, task,
agent, or packet. It returns
`urn:crewfold:schema:local-api:context-refresh-result:v1`, type
`context_refresh`, and status `created|pending|up_to_date|rebase_required` plus
the exact run/base packet, durable state revision, inspected event interval,
chain state, optional immutable delta, optional rebase reason, and
`event_sequence`. The event sequence identifies the built/rebase fact and is zero
for pending, up-to-date, and unsupported-old-packet compatibility results. A
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

Each delta is immutable, based on packet v4, and capped at 16 KiB; the chain is
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
checkout revisions and cannot be reused by another run. A prebuilt v4 packet also
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
Existing packet-v3 bytes remain unchanged. See
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
packet-v5 snapshot, capability, expiry, proposal kind, and target-profile set at
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
packet v5, the run and pending job, and context/capability bindings for the
existing exact active assignment. It returns `manager_invocation`: the exact grant
and profile, complete run detail, and final event sequence. Ambiguity, an inactive
grant/profile, stale revision or assignment, scope mismatch, or a planning profile
not bound to the grant fails before any runtime launch.

The run can submit only the proposal kinds frozen in packet v5. One action array
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
the active unexpired grant, immutable source packet-v5/grant tuple, current source
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

`approval.list` filters by status/action and `approval.inspect` returns one exact
request. `approval.allow` and `approval.deny` require the pending request's
expected revision, a bounded decision note, and an idempotency key. The result type
`approval_mutation` returns both approval and bound supervisor action plus event
sequence. One action has at most one approval; a stale or repeated decision
returns a conflict rather than applying twice. An allow authorizes only that
closed action at its frozen revision and never becomes a reusable grant.

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
