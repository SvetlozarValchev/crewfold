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
context-packet build, live-context delta, and acknowledgement facts are also
implemented. Owner-granted manager authority/profile lifecycle and manager
proposal submission/decision facts are implemented; supervisor/approval facts
listed below are part of the M16 durable contract as their store lane lands.
Names for checks and external integrations remain proposals; the catalogue
defines intended coverage, not a frozen schema.

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
task.reassigned
task.role_designated
task.claim_requirement_created
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
contradiction.detected
contradiction.confirmed
contradiction.dismissed
contradiction.resolved
contradiction.confirm_denied
contradiction.dismiss_denied
knowledge.imported
contradiction.imported
knowledge.import_completed
context.packet_built
context_delta.built
context_delta.acknowledged
context_delta.rebase_required
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
IDs live in packet v4 rather than being duplicated into the event.

`context_delta.built` identifies the exact run/base packet, immutable delta ID and
run-local sequence, exclusive/inclusive event interval, content hash, byte size,
and resulting state revision. The event links to the stored delta; it does not
duplicate message, knowledge, contradiction, roster, or dependency bodies.
`context_delta.acknowledged` links the exact `cdack_...` receipt, run, delta,
sequence, and resulting state revision. It can be emitted only by the exact bound
live run. Exact idempotent acknowledgement replay emits no second fact.

`context_delta.rebase_required` freezes the run/base packet, inspected cursor,
stable reason, and resulting state revision when safe incremental delivery is no
longer possible. It is emitted once on the transition into durable rebase state;
refresh replay does not repeat it. An `up_to_date` refresh advances only delivery
bookkeeping and deliberately appends no event, avoiding self-triggered scan churn.
Owner refresh while a delta is pending likewise returns the existing object and
appends no journal fact; its command idempotency receipt is still persisted.

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

`contradiction.detected` records the canonical exact pair, project, both task
scopes, proposed state, and reporter actor without embedding either immutable
body. `contradiction.confirmed` and `contradiction.dismissed` advance the
contradiction state and link the exact owner authority check. Internal non-owner
governance attempts commit `contradiction.confirm_denied` or
`contradiction.dismiss_denied` plus their denied authority record without changing
state. The reserved run-MCP confirmation probe stops earlier and emits only
`run.tool_denied`.

`contradiction.resolved` is emitted atomically when a normal owner-governed stale
or supersede event makes one participant terminal. It names the participant,
resolution reason, and exact cause event. Derived `knowledge.dispute` queries,
search filtering, and failed context builds append no event. No
`knowledge.disputed` fact exists because effective dispute is relational open
state, not knowledge currency.

Portable export is a read and appends no event. A first successful owner import
appends one `knowledge.imported` attestation per exact restored revision, one
`contradiction.imported` attestation per restored contradiction, and a final
`knowledge.import_completed` event for the immutable receipt/bundle digest.
These are local import facts, not replays of events from another node. Origin
events and authority-check ledgers are excluded from portable v1. Exact replay of
an already imported bundle appends nothing.

The following remain proposals:

```text
artifact.registered
artifact.redacted
context.packet_dispatched
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

## Manager proposals and deterministic supervision

The implemented manager facts are:

```text
manager.grant_created
manager.grant_revoked
manager.launch_profile_created
manager.launch_profile_retired
manager.proposal_submitted
manager.proposal_accepted
manager.proposal_rejected
manager.proposal_stale
task.claim_requirement_created
supervisor.intent_created
supervisor.intent_satisfied
supervisor.intent_failed
supervisor.intent_cancelled
```

Grant/profile facts identify the exact entity/revision and minimal scope/hash;
the immutable normalized authority and scenario remain in their canonical rows.
`manager.proposal_submitted` is an agent-run fact linked to the sealed proposal
submission and exact action count/hash. It has no work effect.
`manager.proposal_accepted` is a local-owner fact committed with the complete
typed effect set; `manager.proposal_rejected` is a local-owner decision with no
work effects. Failed current-state revalidation records
`manager.proposal_stale`, its exact validation diagnostics, and no effects.
Accepted claim requirements and scheduling intents append their own exact creation
facts alongside the ordinary task/dependency facts. A definitive completion
closes the intent with `supervisor.intent_satisfied`; rejected completion or
runtime failure closes it with `supervisor.intent_failed`; a stopped run closes it
with `supervisor.intent_cancelled`. A definite `start_failed` retains the original
`run_requested` intent only while another exact bounded retry remains authorized;
fresh retry runs keep that intent until the latest receipt-linked successor is
definitive. Disabling or exhausting retry closes it failed. Owner task
cancellation closes a pending/deferred intent, or a `run_requested` intent whose
exact latest retry-chain run is definitively `start_failed`, with one local-owner
`supervisor.intent_cancelled` in the same task transaction. Invalid proposal
submission is still an immutable submission fact, not an applied plan. Exact
idempotent replay appends none of these a second time.

The M16 supervisor fact set is:

```text
supervisor.policy_configured
supervisor.action_recorded
supervisor.action_applied
supervisor.scan_completed
approval.requested
approval.granted
approval.denied
approval.consumed
```

A policy fact identifies the new immutable revision. An action-recorded fact
freezes condition/response, entity and policy revisions, canonical condition key,
constraint snapshot hash, and journal cursor; it is evidence, not necessarily an
effect. `supervisor.action_applied` requires the same action plus either the exact
automatic dependency-ready scheduling rule or its granted/consumed approval.
Approval facts always identify one exact supervisor action and approval revision.
One decision cannot be reused for a second action. Queries, explanations,
capacity deferrals, and exact idempotent replays append no second fact.
`supervisor.scan_completed` seals the scan's input/output cursor and bounded result
receipt when a pass records material action state. A pass first captures a fixed
journal cutoff and classifies closed-union pages of at most 1,000 events. It may
advance an understood partial-page cursor without an event, but cannot act until
it reaches that captured cutoff. An unknown event type fails with
`unsupported_supervisor_event` before cursor, effect, event, or idempotency
commit. Owner-facing no-op commands retain a durable idempotency receipt but no
scan event; internal daemon no-ops retain neither receipt nor event. Read-only
explanations still append no fact.

The scheduling transaction also emits the ordinary assignment, context-packet,
run-request, and task facts. Those ordinary facts remain the work-state journal;
the supervisor action and immutable scheduling receipt explain why that exact run
was authorized. Runtime launch occurs only after their transaction commits, so a
crash before launch creates no invented `run.started` fact and restart reconciles
the same durable run.

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
approval.consumed
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
