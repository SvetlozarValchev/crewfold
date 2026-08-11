# MCP tool contract

Status: proposed interface for the first implementation.

## Principles

- The server authenticates the run outside normal tool arguments.
- Every response includes stable entity IDs and revisions where relevant.
- Mutations are idempotent or accept an idempotency key.
- Tool results remain concise and link to resources for larger records.
- A tool cannot exceed the run's task, project, or action scope.
- Human approval is represented as a pending result, never bypassed.

## Resources

Proposed resource identifiers:

```text
crewfold://runs/{run_id}/briefing
crewfold://tasks/{task_id}
crewfold://tasks/{task_id}/evidence
crewfold://agents/{agent_id}/inbox
crewfold://threads/{thread_id}
crewfold://meetings/{meeting_id}
crewfold://knowledge/{knowledge_id}/revisions/{revision}
crewfold://context-packets/{packet_id}
crewfold://projects/{project_id}/status
```

Resource reads enforce the same scope as tools. A URI is not a capability by
itself.

## Required tools

### `crewfold_get_briefing`

Returns the immutable base context packet and any acknowledged deltas for the
authenticated run.

Input:

```json
{}
```

Output includes run, role, task contract, project/checkout snapshot, applicable
knowledge, dependencies, claims, inbox summary, policy, and reporting instructions.

### `crewfold_get_status`

Returns the current task/run status, budgets, dependencies, claims, and pending
requests relevant to this run.

### `crewfold_report_progress`

Records a concise checkpoint.

```json
{
  "summary": "Contact cache implemented; determinism test still failing",
  "completed": ["cache structure", "unit tests for insertion"],
  "next": ["diagnose iteration ordering"],
  "risks": [],
  "evidence_ids": ["artifact_id"],
  "idempotency_key": "provider-turn-or-generated-key"
}
```

Progress does not complete the task and is not automatically durable knowledge.

### `crewfold_report_blocked`

Records why work cannot safely continue and what resolution is requested.

```json
{
  "reason": "Dependency task has not defined the serialized key format",
  "needs": "Answer from schema-owner or accepted interface decision",
  "severity": "normal",
  "related_ids": ["task_id"]
}
```

### `crewfold_propose_completion`

Submits a handoff and evidence for acceptance/review.

```json
{
  "summary": "Implemented deterministic contact cache",
  "deliverables": [
    {"name": "ordering tests", "status": "satisfied", "evidence_id": "artifact_id"}
  ],
  "changed_paths": ["src/physics/contact/cache.go"],
  "checks": [{"name": "contact tests", "status": "passed", "artifact_id": "artifact_id"}],
  "remaining_risks": [],
  "knowledge_proposal_ids": []
}
```

The response reports `accepted`, `review_required`, `changes_requested`, or
`approval_required`. It never fabricates task completion from process exit.

### `crewfold_list_messages`

Lists unread or filtered mailbox messages visible to the run. Bodies may be
summarized in the list; the full message is a resource.

### `crewfold_send_message`

```json
{
  "to": ["agent_or_team_id"],
  "kind": "question",
  "subject": "Serialized contact key format",
  "body": "Which byte ordering is authoritative for the cache key?",
  "related_ids": ["task_id", "decision_id"],
  "acknowledgement_required": true,
  "idempotency_key": "generated-key"
}
```

Recipients must be within communication policy. Broadcast and messages to humans
may require stronger authority.

### `crewfold_acknowledge_message`

Records that the run received a message and optionally whether it can act on it.

### `crewfold_claim_scope`

Requests a leased read/write/advisory claim.

```json
{
  "mode": "exclusive_write",
  "subjects": [
    {"kind": "path", "value": "src/physics/contact/**"}
  ],
  "lease_seconds": 3600,
  "reason": "Implement task deliverables"
}
```

The server may grant, deny, shorten, or mark the claim advisory. A conflict response
links the existing claim and available resolution action.

### `crewfold_release_claim`

Releases a claim explicitly. Task completion also requests release through domain
logic; it does not delete the claim history.

### `crewfold_publish_artifact`

Registers evidence already present in an allowed location or provides a bounded
text artifact. The server validates paths and applies retention/redaction policy.

### `crewfold_propose_knowledge`

Submits a candidate finding, risk, glossary item, summary, or decision for curation.
It must cite evidence or explain that it is unverified. The tool response clearly
states that proposal is not acceptance.

### `crewfold_request_meeting`

Proposes a meeting with one agenda and relevant participants/evidence. The
supervisor may create a thread instead, request more information, or require owner
approval.

## Optional manager tools

Available only to appropriately scoped manager runs:

- `crewfold_propose_tasks`
- `crewfold_propose_assignment`
- `crewfold_request_review`
- `crewfold_resolve_overlap`
- `crewfold_propose_context_revision`
- `crewfold_escalate_to_owner`

Manager tools normally create proposals. Execution remains subject to deterministic
policy, capacity, dependency, and claim checks.

## Error shape

Tool errors should distinguish:

```text
invalid_input
not_found
revision_conflict
out_of_scope
denied_by_policy
approval_required
claim_conflict
budget_exhausted
dependency_blocked
temporarily_unavailable
internal_error
```

An error includes a safe explanation, a retryability flag, and related entity IDs.
Internal filesystem paths or secrets are omitted when outside the caller's scope.
