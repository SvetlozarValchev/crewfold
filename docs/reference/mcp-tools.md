# MCP tool contract

Status: implemented run-scoped subset; messaging, claims, knowledge, meetings, and
manager/outcome tools remain planned.

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

## Idempotency and audit

Report and artifact idempotency is local to the authenticated run. Repeating the
same key and content returns the same durable record while the capability remains
active, including after the worker has applied a progress report. Reusing a key
with different content returns
`invalid_input` without another mutation.

Allowed tools/resources append `run.tool_called`; denied scope probes append
`run.tool_denied`. Reports and artifacts append `run.report_received` and
`run.artifact_published`. Audits contain request/method/target/outcome/error code,
not capability tokens or arbitrary request bodies.

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

Mailbox, send/acknowledge, claims, knowledge proposals, meetings, manager actions,
outcome assessments, and project briefings are deliberately absent. Their future
URIs and tools will use the same scope, idempotency, audit, and domain-authority
rules; M7 does not reserve unchecked behavior merely by documenting a name.
