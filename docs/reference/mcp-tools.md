# MCP tool contract

Status: implemented run-scoped briefing, reporting, artifact, durable mailbox,
canonical-knowledge proposal, contradiction report, live-context
fetch/acknowledgement, owner-granted manager-proposal tools, and the current
check-watch surface. Claims and meetings remain outside this MCP surface.
Outcome commitments, assessments, checkpoints, and management briefings are
implemented only on the owner-local CLI/API and deliberately have no MCP tool.

## Transport and authentication

The daemon accepts newline-delimited JSON-RPC 2.0 MCP requests on the same
owner-only Unix socket as the local API. MCP clients negotiate protocol version
`2025-06-18`. There is no TCP listener.

Every request includes an unguessable run capability in
`params._meta["crewfold/capability"]`. The capability identifies the run outside
ordinary tool arguments; tools therefore accept no caller-selected run, task,
agent, project, or checkout ID. The server verifies the HMAC signature, durable
expiry, and live run state before dispatch. A stopped, completed, failed, expired,
or malformed capability is denied.

The direct provider receives the socket and a private capability-file path through
an allowlisted environment. The token itself is absent from SQLite, the immutable
runtime launch specification, and Crewfold-generated log metadata/API results.
Node key, capability directory, and token files use owner-only permissions. A
malicious same-user provider could still read and print its token; process
containment is a separate boundary.

Tool discovery and invocation are both intersected with the immutable current
packet's `allowed_tools`. Every packet records the base tools, bounded
inbox/participant snapshot, live bounds, source event cursor, and any explicitly
selected accepted knowledge.

Manager runs carry the complete exact active `management_grant` snapshot:
grant/project/objective, manager planning task and agent revisions, proposal kinds,
target launch-profile/agent revision tuples, allowed claim kinds, quantitative
limits, content hash, and expiry. Their `allowed_tools` is the base set plus only
the proposal tools corresponding to those kinds. A packet without that exact
grant receives no manager tools, even when its agent has the same name or
arbitrary `role` label as a grantee. The store rechecks the live
grant and exact run binding on every proposal call; revocation or expiry denies a
later call from an already-running process.

Check-watcher runs carry one complete exact active `check_watch_grant` snapshot:
grant/project, exact agent revision, exact
definition revisions, closed `run|inspect|propose_repair` operations, quantitative
limits, content hash, and expiry. It cannot coexist with a manager grant. A packet
without it receives no check tools. The store rechecks the live
run, packet, exact current grant/revision/expiry, enabled agent revision, project,
operation, and allowlisted definition on every mutation.

`AgentDefinition.Role` and `LaunchProfile.Purpose` are never authorization inputs.
A role such as `CI watcher`, `reviewer`, or `integrator` grants nothing, and any
arbitrarily named agent may receive the exact owner grant.

## Implemented resources

```text
crewfold://runs/{authenticated_run_id}/briefing
crewfold://context-packets/{bound_packet_id}
```

The briefing combines the immutable packet with current run/task state and the
capability expiry. The context-packet resource never changes. Any other URI,
including a valid-looking URI for another run, returns `out_of_scope` and appends
an audited denial. A URI is never a capability by itself.

## Implemented tools

### `crewfold_get_briefing`

Input is `{}`. It returns the authenticated run, current task, immutable packet,
expiry, and briefing resource URI.

### `crewfold_get_status`

Input is `{}`. It returns run/task IDs, states, revisions, current budget, any
blocked question, and a `context` object. The context state reports base packet
ID/schema/cursor, durable state revision, inspected-through cursor, status,
optional pending delta ID/sequence, and optional rebase reason. It does not mutate
coordination state.

### `crewfold_get_context_delta`

Input is exactly `{}`. It fetches the sole owner-built pending delta for the
authenticated run, or returns `none_pending`/`rebase_required` state. It cannot
scan events, construct a delta, select another scope, or supply a cursor. The
result includes the exact run/base, durable chain state, optional immutable delta,
and optional rebase reason.

The delta contains whole typed changes and carries its own workspace/project/task/
agent scope, base packet/schema, run-local sequence/parent, exclusive `from` and
inclusive `through` journal cursors, evaluation time, inclusion/exclusion
explanations, total/chain byte budgets, content hash, and byte size. Message
changes contain only `InboxSummaryItem.body_preview`; the full body remains behind
`crewfold_read_message`.

### `crewfold_acknowledge_context_delta`

```json
{
  "delta_id": "cdelta_0123456789abcdef0123456789abcdef",
  "expected_sequence": 1,
  "idempotency_key": "incorporated-context-1"
}
```

The server derives the run from the capability and accepts only that live run's
exact sole pending delta and sequence. A success returns one immutable
`cdack_...` receipt. An exact retry returns the same receipt and appends no second
acknowledgement event; key reuse with different input fails. An owner cannot call
this operation through the local API, and fetching alone does not attest
consumption.

### `crewfold_report_progress`

```json
{
  "summary": "Contact cache implemented; determinism test still failing",
  "completed": ["cache structure", "insertion tests"],
  "next": ["diagnose iteration ordering"],
  "risks": [],
  "evidence_ids": ["artifact_id"],
  "idempotency_key": "provider-turn-or-generated-key"
}
```

The report is normalized into a durable pending run report. The run worker applies
it through the same run/task transition used by other providers. Progress cannot
complete a task or become accepted shared knowledge.

### `crewfold_report_blocked`

```json
{
  "reason": "The serialized key format is undefined",
  "needs": ["owner or schema-owner decision"],
  "severity": "blocking",
  "related_ids": ["task_id"],
  "idempotency_key": "provider-turn-or-generated-key"
}
```

Severity is `blocking`, `high`, `medium`, or `low`. A newly submitted blocked
report becomes authoritative only when the worker applies the normalized
observation.

### `crewfold_publish_artifact`

```json
{
  "name": "test evidence",
  "media_type": "text/plain",
  "content": "all deterministic contact tests passed",
  "idempotency_key": "artifact-key"
}
```

This first form accepts only bounded UTF-8 text up to 32 KiB. It returns an
artifact ID, content hash, and byte size; it does not grant arbitrary filesystem
reads.

### `crewfold_propose_completion`

```json
{
  "summary": "Implemented deterministic contact cache",
  "handoff": "Review the ordering contract and attached evidence.",
  "evidence_ids": ["tests_passed"],
  "changed_paths": ["src/physics/contact/cache.go"],
  "checks": ["contact tests passed"],
  "remaining_risks": [],
  "unknowns": [],
  "idempotency_key": "completion-key"
}
```

The tool submits a proposal; it cannot set the task or run state directly. The run
worker waits for the process to settle, evaluates the scenario's evidence gate,
then either creates the accepted handoff or leaves the task in
`changes_requested`. Process exit alone is not completion authority.

### `crewfold_list_inbox`

Input is `{"limit": 20}` with a required limit from 1 through 50. The authenticated
run normally receives only messages for its assigned agent that are unscoped or
belong to the run's project. An owner-created participant thread is the bounded
exception: a message is also visible when the run's agent, project, and task match
one exact thread binding. Listing changes `queued` messages to `delivered` for
that run; it does not mark them read or acknowledged.

### `crewfold_read_message`

```json
{"message_id":"msg_id","idempotency_key":"provider-turn-key"}
```

Marks one inbox message read. The message must belong to the authenticated agent
and be visible either in the run's project or through its exact participant-thread
binding. Repeating the same scoped key and input returns the original mutation.

### `crewfold_send_message`

```json
{
  "recipient_agent": "engine-review",
  "kind": "review_request",
  "subject": "Review ordering contract",
  "body": "Please inspect the attached evidence.",
  "artifact_ids": ["artifact_id"],
  "idempotency_key": "provider-turn-key"
}
```

The sender identity, run, task, and project come from the authenticated capability;
they cannot be selected in tool input. A send creates a new thread unless
`thread_id` is supplied. Replies may also supply `reply_to_message_id`. Direct
threads remain project-scoped. A run may supply the ID of an owner-created
participant thread to communicate across projects only when both exact
agent/task/project bindings already exist. Runs cannot create that thread or
invite participants. The target is exactly one enabled agent, self-addressing and
human/broadcast recipients are denied, and no message fans out to the roster.
Bodies are limited to 4096 UTF-8 bytes. Direct messages may link at most 16
artifacts published by the sender run. Participant-bound messages accept no
artifacts in this slice; cross-project artifact authority is not inferred from
roster membership.

### `crewfold_acknowledge_message`

```json
{"message_id":"msg_id","idempotency_key":"provider-turn-key"}
```

Acknowledges one visible recipient message, including one authorized by an exact
participant-thread binding. Acknowledging also establishes any missing
delivered/read timestamps; it never changes the immutable message body.

No independent roster tool is exposed to runs. The owner can inspect bindings
through the local API/CLI. The current context packet includes bounded whole authorized
rosters; later thread creation/invitation can arrive as a whole
`participant_roster_updated` delta. Full message bodies and mailbox state
transitions remain explicit mailbox tool calls, and the roster grants no authority
to a different task for the same agent.

### `crewfold_propose_knowledge`

```json
{
  "type": "finding",
  "title": "Contact ordering is byte-stable",
  "body": "The deterministic fixture verifies byte-order emission.",
  "confidence": "high",
  "verification_status": "verified",
  "freshness_policy": "until_superseded",
  "idempotency_key": "contact-ordering-finding"
}
```

The authenticated run and its task fix the actor, workspace, project, and primary
provenance source. Optional `task_scope_id` narrows applicability;
`supersedes_revision_id` proposes a successor; `expires_at` freshness additionally
requires `fresh_until`. The result remains proposed until the local owner accepts
it. No accept, reject, or stale tool is advertised to a run.

### `crewfold_report_contradiction`

```json
{
  "left_revision": "krev_0123456789abcdef0123456789abcdef",
  "right_revision": "krev_fedcba9876543210fedcba9876543210",
  "reason": "The two accepted routing decisions require incompatible orders.",
  "idempotency_key": "routing-order-conflict"
}
```

The tool reports a conflict between two distinct exact accepted/current revisions.
It exposes no actor, run, workspace, project, task, status, or governance field.
The store derives the authenticated live run inside the report transaction and
requires each participant to be project-wide or scoped to that run's exact task.
Revision order is canonicalized for request hashing and identity. The structured
result is the complete proposed contradiction detail with both exact revision
snapshots and its initially empty bounded authority ledger.

A report is only a proposal and does not quarantine either revision. The local
owner must confirm it through the owner-only local API. The known name
`crewfold_confirm_contradiction` is deliberately not advertised or included in a
run capability; probing it produces durable `run.tool_denied` and never creates a
contradiction authority check. The report reason is valid UTF-8 without NUL and is
bounded to 2048 encoded bytes.

### `crewfold_propose_tasks`

```json
{
  "summary": "Split implementation from independent review",
  "actions": [
    {
      "type": "create_task",
      "create_task": {
        "task_key": "implementation",
        "launch_profile_id": "lprof_...",
        "title": "Implement the change",
        "priority": 50,
        "budget": {"token_limit": 10000, "cost_cents": 0, "time_seconds": 3600}
      }
    },
    {
      "type": "add_dependency",
      "add_dependency": {
        "task": {"proposal_task_key": "review"},
        "depends_on": {"proposal_task_key": "implementation"}
      }
    }
  ],
  "idempotency_key": "planning-turn-1"
}
```

This submits a `task_decomposition` proposal. Its closed action set is
`create_task`, `add_dependency`, and `declare_claim_requirement`. Task references
select either an exact existing task/revision or a proposal-local task key, never
an agent-selected project/objective. Claim requirements are limited to the
grant's exact `path|component|operation` set. Every created task names one exact
owner-authored target launch profile.

### `crewfold_propose_assignment`

This submits an `assignment` proposal containing only `assign_task` actions. Each
action binds an exact existing task/revision to one exact target launch profile.
It does not pick by role, purpose, or a ranked candidate set and it does not assign
or launch the task before owner acceptance and a supervisor scheduling pass.

### `crewfold_propose_review`

This submits a `review` proposal containing only `request_review` actions. A
review action specifies its review task content/budget and one exact target launch
profile. `implementer` and `reviewer`, where displayed as task duties, remain
proposal workflow metadata; they are not enumerated `AgentDefinition.Role`
authorities.

### `crewfold_propose_escalation`

This submits an `escalation` proposal containing only `request_action`. The
response is one closed value (`resume_run`, `stop_run`, `retry_task`, or
`reassign_task`) with its exact target IDs/revision and a bounded reason. It is a
request for owner/supervisor handling, never open-ended control of another run.

All four tools take exactly `summary`, one through 32 actions within the grant's
stricter count/budget limits and the 49,152-byte encoded proposal bound, and
`idempotency_key`. Crewfold assigns action IDs
and ordinals; caller-supplied identity is rejected. A success returns the complete
immutable `ManagerProposal`, initially `pending` or `invalid`, with its source run,
exact grant revision, journal high-water, validation issues, and content hash.
Submission never mutates tasks, dependencies, claims, assignments, or runs.
Only the local owner can accept or reject it.

The known name `crewfold_accept_manager_proposal` is deliberately never
advertised. Calling it records a denied scoped-tool audit and returns
`denied_by_policy`; it cannot reach proposal decision state. A manager call also
fails closed when the packet's grant, live run/capability, proposal kind, exact
target profile, claim kind, revision, scope, or quantitative envelope differs.

Newly built context packets include a bounded summary of at most ten queued or
delivered messages for the assigned agent and project. Full message bodies remain
outside the base packet and are retrieved explicitly through the mailbox tools.

## Check-watch tools

The following tools are advertised only when the exact operation is present in
the current packet's grant. Their trusted scope always comes from the authenticated run.

### `crewfold_run_check`

Input is exactly:

```json
{"requirement_id":"checkreq_...","idempotency_key":"watch-unit-1"}
```

The requirement must be active in the grant's project and bind an exact active
definition revision listed in the grant. Checkout is derived from the requirement
task's currently reserved run, then its latest run in stable order. The agent
cannot select workspace, project, actor, checkout, command, arguments, stdin,
environment, profile, grant, evidence class, or recipient.

Success returns the one durable requested check run. It does not assert a pass,
complete a task, or create repair work.

### `crewfold_list_check_results`

Input contains only bounded `limit` and optional opaque `cursor`. The result is
limited to the authenticated grant project and definitions. Each item keeps
outcome, current freshness, requirement state, HEAD and observation times
separate. `missing`, `stale`, and `unknown` are explicit values rather than
omitted or summarized as verified.

### `crewfold_inspect_check_result`

Input is exactly one `check_run_id`. Scope is accepted only when the check run's
project and exact definition revision are visible to the current grant. The
response exposes the frozen criterion, definition, launch receipt, process
outcome, clean/dirty HEAD observations, append-only freshness, bounded redacted
artifact metadata, forced `mechanical_check` evidence, and route/repair status.
It grants no artifact filesystem path and no raw runtime capture.

### `crewfold_propose_check_repair`

Input contains one exact `check_result_id`, bounded `rationale`, and
`idempotency_key`. It additionally requires the grant operation
`propose_repair`, an enabled current project policy, the latest exact trusted
failed result at the current fresh source, and the policy's exact active repair
profile. Timed-out, start-failed, or unknown outcomes and stale or unknown
freshness remain inspectable but cannot seed a repair.

The caller cannot name an agent, task, profile, budget, command, dependency,
claim, or scheduling response. Success creates one inert proposal only. The local
owner must accept it through the local API before a linked repair task and
scheduling intent exist. The proposal freezes the exact authenticated watcher
run, agent revision, and grant revision; no role or purpose label participates.

The known name `crewfold_accept_check_repair` is never advertised. Calling it
records `run.tool_denied` and returns `denied_by_policy`.

Revocation or expiry denies new calls and mutation replays. An exact launch
receipt already committed before revocation remains sufficient only to reconcile
that same sealed direct-runtime operation; it is not a standing grant.

Check results are always mechanical evidence for one named criterion. They never
become independent review or policy acceptance and cannot push, merge, deploy,
complete a task, or choose integration order.

## Idempotency and audit

Report, artifact, message-send, read, acknowledgement, knowledge-proposal,
contradiction-report, manager-proposal, check-run, check-repair-proposal, and
context-delta acknowledgement
idempotency is local to the authenticated actor/run.
Repeating the same key and
content returns the same durable record while the capability remains active,
including after the worker has applied a progress report. Reusing a key with
different content returns `invalid_input` without another mutation.

Allowed tools/resources append `run.tool_called`; denied scope probes append
`run.tool_denied`. Reports and artifacts append `run.report_received` and
`run.artifact_published`; a successful knowledge proposal also appends
`knowledge.proposed`. A reserved governance-tool probe never reaches knowledge
state and is recorded only as `run.tool_denied`. Audits contain
request/method/target/outcome/error code, not capability tokens or arbitrary
request bodies. A successful fresh contradiction report also appends
`contradiction.detected`; its canonical reversed-pair retry appends neither a
second contradiction nor a second `contradiction.detected` fact. The retry's
tool invocation still has its own `run.tool_called` audit.
A fresh delta acknowledgement also appends `context_delta.acknowledged`; an exact
receipt replay appends only its separately audited tool invocation. Delta build
and rebase events are owner-refresh facts rather than MCP effects.

Codex connects through Crewfold's local STDIO bridge. Codex receives only the
socket path and private capability-file path as forwarded environment variables;
the bridge reads the token and injects `crewfold/capability` into requests sent to
the owner-only Unix socket. Client notifications are consumed locally because the
daemon's scoped request contract requires an ID. Neither STDIO direction contains
the token.

## Error vocabulary

The implemented scoped surface uses exactly:

```text
invalid_input
out_of_scope
denied_by_policy
temporarily_unavailable
```

Errors include a safe message, retryability flag, and optional related IDs. MCP
authorization/resource failures are JSON-RPC errors; tool-domain failures are MCP
tool results with `isError: true` and the same structured body.

## Deferred tools and resources

Claims, meetings, outcome assessments, and project briefings are deliberately
absent. Outcome commitments, assessments, checkpoints, and management briefings
are owner-local operations and do not have agent or run-scoped MCP variants.
Knowledge governance remains an owner-only local API. Manager proposal
acceptance, launch-profile/grant mutation, supervisor policy/action control, and
approval decisions likewise remain owner-only local API operations. Check
definition/requirement/grant/route/policy mutation, repair acceptance, and
task/outcome acceptance likewise remain owner-only. Thread closing,
multi-recipient conversation, runtime-specific live prompting, and human-directed
messages are also not exposed by the current mailbox surface.
