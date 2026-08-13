# Milestone review — owner-granted managers and deterministic supervision

## Identity

- Milestone: `M16 — Manager proposals and deterministic supervision`
- Review status: `pending final acceptance gate`
- Final implementation commit: `pending`
- Reviewer: `automated acceptance and independent adversarial review`
- Date: `2026-08-13`

## Demonstrable outcome

Crewfold can grant one exact planning run a bounded proposal capability without
delegating owner or launch authority. A packet-v5 manager may submit a closed,
typed plan for tasks, dependencies, claim requirements, exact-profile
assignments, review, or escalation. The plan is immutable and inert until the
local owner accepts its exact revision. Acceptance either applies the complete
validated action graph and durable scheduling intents or applies nothing.

A deterministic local supervisor can then reconcile existing runs, evaluate
accepted intents under one immutable owner policy revision, and schedule only an
eligible dependency-ready task. Every placement uses an exact owner-authored
launch profile and commits its assignment, packet, run, pending job, action,
receipt, events, and idempotency result before external runtime launch. Other
conditions become explainable owner approvals rather than hidden autonomous
control.

## Public capability

- `manager grant create|revoke|show|list` and `launch-profile
  create|retire|show|list` expose exact owner delegation and scheduling
  eligibility.
- `manager propose-tasks` invokes the exact grant-bound planning tuple;
  `proposal list|inspect|accept|reject` keeps model proposal and owner decision
  separate.
- Packet v5 advertises only `crewfold_propose_tasks`,
  `crewfold_propose_assignment`, `crewfold_propose_review`, and
  `crewfold_propose_escalation` when their exact kinds are granted. The recognized
  `crewfold_accept_manager_proposal` name is always denied.
- `supervisor policy show|update`, `supervisor run|list|explain`, and `approval
  list|inspect|allow|deny` expose deterministic policy, frozen actions,
  constraints, and exact owner decisions.

## Authority and integrity proof

- Owner-selected `AgentDefinition.Role` values are descriptive strings, not an
  enum or authority source. The acceptance fixture uses the same arbitrary role
  label for two agents; only the exact grant/task/assignment/planning-profile/
  packet-v5 run receives proposal tools.
- Target launch profiles exist before the grant and are frozen as exact
  project-agent scheduling eligibility. The planning profile binds to the grant
  and cannot also be a target profile. No hidden eligibility table, role match,
  or candidate rank exists.
- Every proposal call rechecks the active unexpired grant, live run/capability,
  packet-v5 snapshot, exact planning binding, kind, target profile revisions,
  claim kinds, open count, and quantitative limits. Revocation makes a later
  call from an already-live run fail closed.
- Submission stores no work effect. Acceptance rechecks exact current scope,
  objective/task/profile revisions, same-project dependencies and acyclicity,
  claim shape/allowlist, count caps, finite/unlimited budget semantics, and the
  49,152-byte encoded action bound in one immediate transaction.
- A sealed pending proposal remains owner-decidable after its planning run
  completes, but acceptance still proves the immutable source packet-v5/grant
  tuple, active unexpired grant, current frozen source-agent revision, and exact
  active objective revision; it neither requires nor restores the released
  planning assignment.
- Only the closed `dependency_ready -> schedule` policy path can auto-apply.
  Bounded cooled-down retry is separately capped at `0..3`. Blocked, stale,
  failed, repeated-failure, wall-time over-budget, stop, resume, and reassign
  actions require one exact approval and cannot be applied by role/name equality.
- Accepted manager escalations become the distinct closed
  `manager_escalation` condition with typed proposal/action provenance. Their
  requested target/revision and response remain inert until one owner approval;
  allow revalidates the target and deny/replay cannot duplicate an effect.
- Requested, starting, active, blocked, stopping, and lost/reserved work consumes
  its global/project/provider/agent capacity until run-first reconciliation.
  Reassignment never releases uncertain work merely to make capacity available.
- Ready intents use priority descending, exact readiness time ascending, then
  task ID. Readiness time comes from intent creation, task-readied or
  assignment-expired facts, and dependency completion—not general metadata
  timestamps. A stable deferral waits 30 seconds; only a newly classified fact
  relevant to its sealed primary failing dimension wakes it early.
- Intent lifecycle is closed and exact: acceptance begins pending, scheduling
  advances to `run_requested`, definitive outcomes produce
  satisfied/failed/cancelled, and a start failure remains open only while another
  bounded retry is authorized. Manual assignment cannot replace an open intent;
  owner cancellation closes pending/deferred work or exact-latest
  start-failed retry work with one `supervisor.intent_cancelled` fact.
- Normalized authority children, typed proposal actions/effects/requirements,
  immutable intent/action/approval/receipt rows, canonical hashes, exact event
  links, and direct-SQL triggers make incomplete or substituted authority graphs
  fail closed.

## Atomicity, recovery, and concurrency proof

- All M16 mutations use SQLite immediate transactions and idempotency receipts.
  Failure after projection, event, intent, or receipt insertion rolls back the
  whole command and permits the same key to succeed later.
- Proposal accept/reject, supervisor passes, and approval decisions serialize
  optimistic revision and uniqueness checks. Racing or replayed calls yield the
  same receipt or a stable conflict; they do not duplicate actions, effects,
  approvals, assignments, intents, or runs.
- A supervisor-origin run cannot be claimed by the worker without its exact
  scheduling receipt. A crash after committed intent/run/job but before launch is
  recovered by the same run ID and idempotent runtime launch boundary.
- That receipt freezes authority for the committed operation. Worker claim/start
  still proves exact run/job/task/active-assignment linkage, but a later profile
  retirement, agent disablement or revision change, or assignment-deadline passage affects
  future placements rather than revoking the already receipted launch. A new
  retry revalidates current authority and receives its own receipt.
- RFC3339 expiry comparisons use canonical instant keys, so equivalent instants
  and non-UTC offsets cannot invert grant, capability, packet, or approval
  authority.
- Schema-17's fixed management SQL is the reviewed transaction-specific
  exception recorded in ADR-0007: it is parameterized, kept beside the atomic
  policy/event/receipt ordering it implements, and covered by canonical-read,
  direct-SQL, rollback, race, restart, and schema-manifest tests. It does not
  establish a new default for unrelated persistence.
- Supervisor automation classifies a closed event union through one captured
  cutoff in pages of at most 1,000. Known partial pages are effect-free and
  restart-safe; an unknown fact returns `unsupported_supervisor_event` without
  advancing the cursor or committing effects. Public no-op replay is sealed by a
  durable receipt, while daemon no-ops produce no receipt or event churn.

## Executable acceptance evidence

The final gate is the matrix in
[ADR-0015](../decisions/0015-owner-granted-manager-proposals-and-deterministic-supervision.md#executable-acceptance-matrix).
The provider-free `test/scenarios/manager-supervisor/run.sh` path now covers inert
submission, arbitrary same-role separation, packet-v4 denial/packet-v5 grant,
owner acceptance/replay, restart with durable pending intents, one explicit
schedule followed by background `A -> B -> independent review` progression, and
live revocation without a second proposal.

Focused executable coverage now includes:

- aggregate cycle/scope/budget/profile/claim validation, exact assignment/review
  targets, all four proposal kinds, escalation allow/deny/replay/stale-target
  behavior, and the complete 49,152-byte canonical proposal boundary;
- exact intent terminalization for completion, rejected completion, runtime
  failure, stopped work, disabled/exhausted retry, owner cancellation, and the
  latest retry-chain start failure;
- priority/readiness ordering, 30-second deferral backoff and relevant-fact wake,
  bounded queue paging, every capacity dimension, stale/lost reservation, exact
  bounded retry, and singular nonautomatic approvals;
- named fault barriers, concurrent/idempotent replay, journal paging/unknown-event
  failure, raw-SQL receipt/action/lifecycle attacks, frozen committed worker
  authority, and daemon restart at prelaunch launch/handle boundaries; and
- packet/schema field parity, typed proposal identity, manager tool scope, retry
  prior/fresh-run distinction, and typed escalation provenance.

This evidence keeps the review at `pending final acceptance gate`; it does not
substitute for one clean stable-tree gate run and independent severity audit.

Before this review changes to `passed`, the stable tree must pass:

- generated database source/output consistency and protocol schema validation;
- formatting, `go vet ./...`, `go test ./...`, and `go test -race ./...`;
- every M0–M16 black-box scenario through `scripts/check.sh`; and
- independent final audit with no unresolved high- or medium-severity authority,
  transaction, recovery, or compatibility defect.

## Compatibility and deferrals

Packets v1 through v4 remain readable, immutable, and unable to propose manager
actions. Existing runs gain no authority after upgrade. M16 makes no network or
model call and does not add provider transcript authority, organization-wide
roles, adaptive/model-driven scheduling, arbitrary manager commands, automatic
reassignment, check-watch execution, outcome scoring, or deployment/push
authority. Check observation is a reusable capability for a later milestone and
may be attached to any eligible agent; it is not a fixed agent role.
