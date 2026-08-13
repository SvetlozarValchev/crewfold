# MCP tool contract

Status: implemented run-scoped briefing, reporting, artifact, durable mailbox, and
canonical-knowledge proposal subset. Claims, meetings, and manager/outcome tools
remain planned.

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

Tool discovery and invocation are both intersected with the immutable packet's
`allowed_tools`. A run created with an older packet therefore does not gain
mailbox or knowledge-proposal authority merely because the daemon binary was
upgraded. New v3 packets record both tool sets, the bounded inbox snapshot, and
any explicitly selected accepted knowledge.

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

Input is `{}`. It returns run/task IDs, states, revisions, current budget, and any
blocked question. It does not mutate coordination state.

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

No roster tool is exposed to runs in this slice. The owner can inspect bindings
through the local API/CLI. Context packet v3 keeps its existing bounded inbox
summary shape and may include authorized participant messages; full bodies and
state transitions remain explicit mailbox tool calls. Roster snapshots and live
context refresh are deferred to packet v4/context deltas.

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

Newly built context packets include a bounded summary of at most ten queued or
delivered messages for the assigned agent and project. Full message bodies remain
outside the base packet and are retrieved explicitly through the mailbox tools.

## Idempotency and audit

Report, artifact, message-send, read, acknowledgement, knowledge-proposal, and
contradiction-report idempotency is local to the authenticated actor/run.
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

Claims, meetings, manager actions, outcome assessments, and project briefings are
deliberately absent. Their future URIs and tools will use the same scope,
idempotency, audit, and domain-authority rules. Knowledge governance remains an
owner-only local API rather than a deferred agent tool. Thread closing,
multi-recipient conversation, runtime-specific live prompting, and human-directed
messages are also not exposed by the current mailbox surface.
