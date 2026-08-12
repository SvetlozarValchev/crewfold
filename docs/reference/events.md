# Event catalogue

Status: the v1 envelope is implemented. Durable facts currently emitted are
`workspace.created`, `project.registered`, `repository.registered`,
`checkout.registered`, `checkout.git_observed`, `agent.created`, `agent.updated`,
`objective.created`, `objective.updated`, `task.created`, `task.updated`,
`task.dependency_added`, `task.assigned`, `task.started`, `task.blocked`,
`task.readied`, `task.cancelled`, and `task.assignment_expired`. Remaining names
and payload details are proposed; the catalogue defines their intended coverage,
not a frozen schema.

## Envelope

Every event has:

```json
{
  "event_id": "opaque-id",
  "sequence": 42,
  "type": "task.assigned",
  "schema_version": 1,
  "occurred_at": "2026-08-12T12:00:00Z",
  "recorded_at": "2026-08-12T12:00:00Z",
  "actor": {
    "actor_id": "opaque-id",
    "actor_type": "human|agent_run|subsystem|integration"
  },
  "workspace_id": "opaque-id",
  "entity": {"type": "task", "id": "opaque-id", "revision": 3},
  "correlation_id": "opaque-id",
  "causation_id": "opaque-id",
  "data": {}
}
```

`occurred_at` is when an external observation claims it occurred. `recorded_at` and
the local `sequence` establish journal order. Clock time never establishes causal
order by itself.

## Workspace and project

```text
workspace.created
workspace.settings_changed
project.registered
project.updated
repository.registered
repository.identity_reconciled
checkout.registered
checkout.git_observed
checkout.write_policy_changed
```

## Agents, teams, and runs

Implemented today: `agent.created` and `agent.updated`. The other names below are
reserved proposals.

```text
team.created
team.membership_changed
agent.created
agent.updated
agent.enabled
agent.disabled
run.requested
run.starting
run.started
run.runtime_observed
run.heartbeat_recorded
run.blocked
run.settling
run.completed
run.start_failed
run.failed
run.stop_requested
run.stopped
run.lost
run.resume_handle_recorded
```

## Objectives and tasks

Implemented today: `objective.created`, `objective.updated`, `task.created`,
`task.updated`, `task.dependency_added`, `task.assigned`, `task.started`,
`task.blocked`, `task.readied`, `task.cancelled`, and
`task.assignment_expired`. The other names below are reserved proposals.

```text
objective.created
objective.updated
objective.completed
task.created
task.updated
task.dependency_added
task.dependency_removed
task.readied
task.assignment_proposed
task.assigned
task.assignment_renewed
task.assignment_expired
task.started
task.progress_reported
task.blocked
task.completion_proposed
task.review_requested
task.changes_requested
task.completed
task.failed
task.retry_requested
task.cancelled
task.handoff_recorded
```

## Claims and overlaps

```text
claim.requested
claim.granted
claim.denied
claim.renewed
claim.released
claim.expired
claim.drift_observed
overlap.detected
overlap.severity_changed
overlap.resolution_proposed
overlap.resolved
overlap.dismissed
```

## Communication and meetings

```text
message.sent
message.delivered
message.read
message.acknowledged
message.delivery_failed
thread.created
thread.closed
meeting.proposed
meeting.created
meeting.context_frozen
meeting.started
meeting.contribution_recorded
meeting.resolution_proposed
meeting.concluded
meeting.stalled
meeting.cancelled
```

## Knowledge and context

```text
artifact.registered
artifact.redacted
knowledge.proposed
knowledge.accepted
knowledge.rejected
knowledge.marked_stale
knowledge.disputed
knowledge.superseded
context_packet.built
context_packet.dispatched
context_delta.built
context_delta.acknowledged
contradiction.detected
contradiction.resolved
```

## Outcomes and owner checkpoints

```text
outcome.assessment_proposed
outcome.assessment_accepted
outcome.assessment_rejected
outcome.assessment_superseded
owner_checkpoint.created
owner_checkpoint.archived
```

Management briefings are rebuildable projections at an event cursor, so querying
or rendering one does not append a fact event. Optional rendering or projector
failures are operational diagnostics unless they change durable coordination
state.

## Policy, budgets, and approvals

```text
policy.changed
policy.decision_recorded
budget.reserved
budget.consumed
budget.exhausted
budget.released
approval.requested
approval.granted
approval.denied
approval.expired
```

## Runtime and integration

```text
run.requested
run.starting
run.started
run.progress_reported
run.blocked
run.resumed
run.completion_proposed
run.completed
run.start_failed
run.failed
task.completion_proposed
task.changes_requested
task.handoff_recorded
task.completed
task.failed
runtime.probed
runtime.surface_created
runtime.surface_reconciled
runtime.operation_failed
provider.probed
provider.session_bound
provider.usage_observed
check.started
check.completed
check.invalidated
external_status.observed
```

## Event discipline

- Events use past-tense facts, not commands or aspirations.
- Sensitive raw output is an artifact, not embedded in every event.
- Event payloads include IDs and minimal immutable facts needed for audit/rebuild.
- New optional fields preserve a schema version; incompatible meaning requires a
  new version or event type.
- Consumers tolerate unknown event types unless they provide a safety-critical
  projection, in which case they stop and report incompatibility.
