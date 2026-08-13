# Event catalogue

Status: the v1 envelope is implemented. Workspace/source/agent/objective/task
coordination events and the run events used by deterministic and direct execution
are implemented, including request, start, runtime binding, progress, blockage,
resume, completion proposal, handoff, completion/failure, stop, lost-runtime,
scoped tool audit, report receipt, artifact publication, and context-packet facts.
Thread creation, durable message send/delivery/read/acknowledgement, and wake
success/failure facts are also implemented. Claim add/release/expiry, overlap
open/resolution, drift open/resolution, and structured meeting facts are
implemented. Canonical knowledge proposal/governance, authority-denial, and
context-packet build facts are also implemented. Names for policy, checks, context
deltas, contradiction handling, and external integrations remain proposals; the
catalogue defines intended coverage, not a frozen schema.

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

Agent create/update and run lifecycle names through `run.lost` are implemented.
Team, heartbeat, settling, and provider resume names remain proposals.

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
run.progress_reported
run.blocked
run.resumed
run.completion_proposed
run.settling
run.completed
run.start_failed
run.failed
run.stop_requested
run.stopped
run.lost
run.resume_handle_recorded
run.report_received
run.artifact_published
run.tool_called
run.tool_denied
```

## Objectives and tasks

Objective create/update and task lifecycle names used by coordination and run
execution are implemented. Unused decomposition/retry names remain proposals.

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
task.run_stopped
```

## Claims and overlaps

The implemented facts are:

```text
claim.added
claim.released
claim.expired
claim.drift_opened
claim.drift_resolved
overlap.opened
overlap.resolved
```

Denial is returned as an atomic command error and deliberately appends no fact
event. Renewal, proposal, dismissal, and severity-change names below remain future
proposals:

```text
claim.requested
claim.granted
claim.denied
claim.renewed
claim.drift_observed
overlap.detected
overlap.severity_changed
overlap.resolution_proposed
overlap.dismissed
```

## Communication and meetings

Thread creation, participant addition, and the six message/wake facts through
`message.wake_failed` are implemented. A participant thread's creation fact freezes
its initial roster and participant revision; later additions are separate facts.
Cross-project sends retain the sender's origin project/task and exact recipient
binding in bounded event data. Structured-meeting creation, checkpoints,
proposals, stalls, conclusion, and takeover are also implemented. Thread closing
and delivery failure distinct from wake failure remain proposals.

```text
thread.created
thread.participant_added
message.sent
message.delivered
message.read
message.acknowledged
message.wake_succeeded
message.wake_failed
message.delivery_failed
thread.closed
meeting.created
meeting.positions_collected
meeting.resolution_proposed
meeting.concluded
meeting.stalled
meeting.human_takeover
```

## Knowledge and context

The implemented facts are:

```text
knowledge.proposed
knowledge.accepted
knowledge.rejected
knowledge.marked_stale
knowledge.superseded
knowledge.acceptance_denied
knowledge.rejection_denied
knowledge.stale_denied
curator.rule_configured
curator.derived
curator.auto_accepted
context.packet_built
```

Proposal events identify the item, project, optional task scope, predecessor, and
source count without embedding the whole body. Accept/reject/stale/supersede facts
advance the knowledge state revision. A non-owner operation that reaches the
internal governance boundary preserves the revision state and commits both its
action-specific denial fact and authority-check record. A run cannot normally
reach that boundary: its unadvertised governance-tool probe is instead captured as
`run.tool_denied` by capability policy.
`context.packet_built` identifies the immutable packet, task, agent, checkout,
semantic hash, and final byte size. Selection details and exact requested knowledge
IDs live in packet v3 rather than being duplicated into the event.

Deterministic `knowledge.search`, `knowledge.index.status`, and
`knowledge.index.rebuild` append no domain event. Search and status are queries;
the FTS5 index is a rebuildable projection rather than canonical history. Rebuild
idempotency is recorded separately and its result identifies the derived
generation/source cursor without implying a knowledge revision changed.

`curator.rule_configured` records one new immutable configuration revision and
whether the exact rule became enabled. `curator.derived` links the rule revision,
accepted structured source revision and its content hash, the new exact knowledge
revision and output hash, without embedding either body in the journal.
`curator.auto_accepted` links that derivation and knowledge revision to the
`kauth_...` check and the earlier `knowledge.accepted` event sequence. Automatic
acceptance therefore emits the normal knowledge fact as well as curator-specific
explanation evidence. Queue queries append no event because the queue is a read
projection. Idempotent process and rule replays append nothing.

The following remain proposals:

```text
artifact.registered
artifact.redacted
knowledge.disputed
context.packet_dispatched
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
