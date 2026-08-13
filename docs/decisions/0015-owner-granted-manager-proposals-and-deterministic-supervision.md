# ADR-0015: Owner-granted manager proposals and deterministic supervision

- Status: accepted
- Date: 2026-08-13

## Context

Crewfold can already represent objectives, dependency-aware tasks, assignments,
claims, immutable context, and restart-safe run intents. Every mutation is still
selected by the local owner, however. A manager agent cannot yet decompose an
objective, and completion of an upstream task does not automatically place ready
dependent work.

Adding those capabilities creates two distinct trust problems. First, a model can
recommend useful work but its prose, role name, provider identity, or confidence
cannot confer local-owner authority. Second, a deterministic supervisor can
execute routine policy, but a crash, stale lease, uncertain runtime outcome, or
concurrent scan must never produce two launches or silently abandon a process
that may still be writing.

The safe boundary is therefore not “the manager is the boss.” It is a four-part
protocol: the owner grants a bounded proposal capability; an authenticated manager
run submits inert typed proposals; the owner accepts or rejects those proposals;
and a deterministic supervisor executes only actions authorized by an exact owner
policy. Manager output and supervisor recommendations remain evidence, not
authority.

## Decision

### Four authorities remain separate

The local owner is the sole authority that creates or revokes manager grants,
agent-bound launch profiles, and supervisor policies. The owner is also the sole
actor that accepts or rejects manager proposals and decides approval requests that
fall outside automatic policy.

An authenticated grantee run has proposal authority only. `AgentDefinition.Role`
is arbitrary owner-defined descriptive metadata; there is no built-in manager,
reviewer, implementer, curator, or watcher authority taxonomy. The database derives
its run, agent, task, workspace, project, objective, packet, and grant from the
run capability; tool arguments cannot select a different actor or scope. An agent
definition whose role string is `manager` has no management authority by itself,
and an agent with any other role string may receive the exact grant.
A manager cannot accept its own proposal, approve a supervisor action, mutate a
grant or policy, invent a launch command, govern knowledge or meetings, accept a
completion, stop or replace an uncertain run, or perform a Git or external side
effect.

The deterministic supervisor is a subsystem actor, not a model identity. It may
apply only an exact action listed by a current owner policy revision. A condition
outside that closed automatic set becomes an immutable approval request with no
operational effect. All other runs retain their existing packet-scoped authority
and acquire no management capability from a role label.

### Packet v5 carries the exact manager grant

Packets v1 through v4 remain byte-compatible and keep their existing tool sets.
In particular, a v4 packet does not gain manager tools even when its agent has any
role label associated with coordination work.

An owner-built manager packet uses
`urn:crewfold:schema:domain:context-packet:v5`. It extends the v4 live-context
contract with one complete `management_grant` snapshot. The snapshot fixes the
grant ID and revision, exact manager agent and assigned task, workspace, project,
objective and their relevant revisions, expiry, allowed proposal kinds, exact
target launch-profile revisions, and numeric task/action/budget ceilings. Its
allowed MCP tools are the v4 tools plus only the proposal tools permitted by that
grant.

Run binding revalidates the current enabled grant and every exact binding. Each
manager proposal call repeats that current/revision/expiry check, so owner
revocation takes effect without trusting a previously decoded packet. Revocation
does not edit historical packet bytes. A replacement grant requires a new packet
and run.

The manager MCP surface is a closed proposal-only set:

- `crewfold_propose_tasks`;
- `crewfold_propose_assignment`;
- `crewfold_propose_review`; and
- `crewfold_propose_escalation`.

The reserved acceptance operation is recognized only to return and audit a policy
denial. Acceptance remains on the owner-local API and CLI.

### Launch authority belongs to owner-defined profiles

Automatic scheduling cannot safely reconstruct a provider launch from model
text. The owner therefore creates revisioned launch profiles. An immutable
profile revision fixes project, runtime and provider, the complete validated
scenario and acceptance contract, process controls, capability lifetime, and
write-mode constraints. A manager grant names the exact profile revisions it may
reference. Neither a manager nor the supervisor can supply an executable, argv,
environment, arbitrary scenario, provider, runtime, or broader tool set.

An accepted task plan binds schedulable work to an exact profile revision. The
profile's project and exact agent revision are the complete scheduling-eligibility
binding; there is no hidden eligibility record or role-derived candidate rank.
Workspace membership and a matching role label are not sufficient placement
authority. Profile purpose and task-role matching are workflow metadata only.
Retiring a profile prevents new placements without rewriting already committed
run intents.

Target profiles are created before the grant and the grant allowlists them. The
separate planning profile used to invoke the grantee binds back to that grant and
is not one of its target profiles. This ordering avoids circular authority: a
manager run can exercise a grant, while a proposed task can cite only an
independently owner-authored target launch profile.

### Proposals are bounded, typed, and inert

One manager proposal belongs to exactly one grant, project, objective, and
authenticated manager run. It is immutable, no larger than 48 KiB (49,152
encoded bytes), and contains
at most 32 ordered actions. The typed union covers task creation, same-project
dependencies, claim requirements, assignment nominations, review requirements,
and owner escalation. Per-kind bounds, unique local aliases, strict field shapes,
exact revisions for existing entities, and canonical ordering make the proposal
independently inspectable and hashable. Cross-project/objective references,
generic command JSON, arbitrary actors, and launch parameters are structurally
invalid.

Submission creates only proposal, action, event, and idempotency records. It does
not create or update tasks, dependencies, assignments, claims, runs, profiles, or
policies. A claim requirement is an inert scheduling constraint rather than an
active leased claim, so a manager cannot reserve a checkout merely by proposing
work. An assignment nomination is a bounded preference rather than a lease, and a
review requirement grants no completion-acceptance authority.

The immutable proposal remains owner-decidable after its planning run completes
and releases the planning assignment. Acceptance does not revive that execution
authority: it revalidates the still-active/unexpired grant, frozen source-v5
run/packet/grant tuple, current source-agent revision, active objective revision,
and all current target profile, graph, claim, and budget references before any
effect.

Owner acceptance uses an expected proposal revision and revalidates the complete
proposal in one immediate transaction. It checks the existing plus proposed DAG,
all exact source revisions, grant and profile allowlists/bindings, task and
objective scope, claim shapes, and budgets. Then it creates all approved work and
the owner decision/application receipt, events, and idempotency result, or none.
Stale input never retargets itself. Rejection preserves the immutable proposal and
creates no work.

Existing budget value zero means unlimited. It must never be interpreted as zero
consumption. A manager may not propose an unlimited dimension beneath a finite
objective or grant ceiling. For each finite objective dimension, the sum of
non-cancelled task allocations plus the proposed allocations must remain within
the objective envelope and the grant's batch ceiling. Token or cost usage affects
supervision only when supplied as monotonic structured data by a trusted adapter;
agent reports and prose cannot manufacture usage evidence.

### The supervisor is a deterministic exception processor

The initial automatic action is `schedule_ready`. It applies only to
owner-accepted work with a current exact agent-bound launch profile. Dependencies
must be complete, no coordination hold may exist, every
claim requirement must be available, and global, project, provider, agent, and
checkout capacity must all be available.

Ready tasks are considered by priority descending, readiness time ascending, and
task ID ascending. Readiness time is the latest of intent creation, an exact
task-readied/assignment-expired fact, and completion of any dependency; metadata
updates do not reset it. Candidate reads are bounded to 100. An unchanged
deferral receives a deterministic 30-second retry time, while only a newly
classified fact matching its latest primary failing dimension can bypass that
backoff. Each accepted action cites an exact profile and therefore one
agent revision; the supervisor does not search or rank agents by role. When
multiple accepted intents are ready, the ready-task order plus current capacity
decides which cited profile is considered first.
Checkout selection retains its write-policy, normalized-path, and ID ordering.
Every scheduled or deferred result freezes the source revisions, policy/profile
revisions, capacity counts and limits, claim/dependency proofs, candidates, stable
reason codes, and human-readable explanation.

The owner policy may additionally enable a bounded same-profile retry after a
definite `start_failed` result, with an exact maximum from zero through three and
a cooldown. Each retry creates a new run/job with a fresh packet and capability,
leaves the failed run immutable, and seals a receipt linking prior/new runs,
action, profile, policy, assignment, and attempt. It requires the exact assignment
and every required claim to remain active and canonically unexpired; retry never
revives expired authority. The action's optional `prior_run_id` identifies that
immutable failed run, while `run_id` identifies the fresh requested run. Blocked
work, failed or repeated-failure recovery, wall-time stop,
reassignment after execution, budget extension, cancellation, meeting/resolution
actions, ordinary resume/stop, and all uncertain outcomes require approval.
Grant/profile/policy changes, arbitrary commands, push, merge, deploy, and
communication with a person are never expressible supervisor actions.

Intent acceptance begins at `pending`; preflight contention may produce
`deferred`, and a committed placement advances the same intent to
`run_requested`. Completion closes it `satisfied`; rejected completion or
definitive failure closes it `failed`; stop closes it `cancelled`. A definite
start failure remains open only while current policy authorizes another exact
bounded retry, and only the latest receipt-linked successor may close the chain.
Owner task cancellation closes pending/deferred work, or retry-pending work only
when that exact latest run is definitively `start_failed`, with one local-owner
`supervisor.intent_cancelled`. Manual assignment cannot replace any open intent.

Every supervisor pass captures one workspace-journal cutoff before consulting
work projections. It classifies a closed union of event types in pages of at most
1,000 and applies no action until it has caught up through that captured cutoff.
An understood partial page may advance only the restart-safe cursor. An unknown
event type returns `unsupported_supervisor_event` and leaves the cursor and every
effect unchanged, so a newer fact cannot be silently skipped by an older binary.
The public command stores even an exact no-op receipt to make replay inert;
background daemon no-ops remain receipt- and event-idle.

An owner-accepted manager `request_action` still has no immediate operational
effect. The scanner converts its exact proposal/action pair into the closed
`manager_escalation` condition and one approval request. Typed
`source_proposal_id`/`source_action_id`, requested response, target revision,
optional reassign profile, and reason are immutable; allow revalidates the target
before applying, while deny or exact replay cannot create a second effect.

`lost` is a reserved rather than reusable state. Runs in `requested`, `starting`,
`active`, `blocked`, `stopping`, and `lost` consume all applicable global,
project, provider, agent, checkout, assignment, and claim capacity. Capacity is
checked and the reservation is committed in the same immediate transaction; a
concurrent supervisor cannot pass a stale count.

### Scheduling is one durable intent before one external effect

Scheduling first preflights the full placement, including every claim
requirement. A deny, pause, or required-resolution conflict creates a deferred
condition with no assignment, claim, context, or run writes.

For a valid placement, one immediate transaction revalidates the task,
dependencies, grant-derived work origin, profile/agent binding, policy, capacities,
checkout, and claim availability. It then creates or renews the assignment,
acquires required claims, builds and binds context, creates the run intent,
capability and worker job, persists the supervisor action and full placement
explanation, appends events, and records idempotency. The runtime driver is called
only after commit.

The supervisor action ID and linked run ID are stable operation IDs. A crash
before the intent commit leaves no partial placement. A crash after commit but
before launch leaves a claimable durable job. A crash after launch but before
handle acknowledgement reconciles the same run ID through the runtime driver's
idempotent launch/reconcile contract. A unique condition key and unique action-to-
run origin prevent concurrent scans or restart from producing a second action.

The receipt freezes launch authority for that one committed operation. Worker
claim/start still proves the exact receipt/job/run, current task state, active
assignment link, and immutable requested event. It does not re-resolve later
profile retirement, agent disablement or revision change, or passage beyond the assignment
deadline as revocation of a run already committed for external launch. Those
facts block a future placement or retry, each of which must revalidate current
authority and seal a new receipt. Reserved-run reconciliation prevents lease
expiry from splitting the committed run from its assignment.

### Runtime reconciliation precedes lease expiry or reassignment

Assignment or claim expiry must not make a task ready or release ownership while
any reserved or lost run exists. Stale requested/starting work first reclaims its
job or reconciles launch. A stale active, blocked, or stopping run is inspected:

- a proven-running process renews its leases;
- a proven-terminal process is normalized before any retry or reassignment;
- a temporarily unavailable adapter retains reservations and retries inspection;
  and
- an unknown identity or outcome becomes `lost`, blocks the task, retains all
  reservations, and requires the owner.

A lost run is never automatically released, expired, reassigned, or retried.
`start_failed` may use only the explicitly enabled same-profile bounded retry.
Failed, blocked, and owner-stopped work has no default automatic restart. An
approval to reassign stale work is itself rejected as stale unless the prior run
has first reached a definite non-lost terminal state. An owner may cancel a task
whose latest retry-chain run is definitively `start_failed`; that same transaction
closes the open intent, preventing a later automatic retry.

### Integrity and explainability fail closed

Authority-significant IDs, scope, revisions, budgets, targets, state, and links
are typed columns or child rows, not trusted only because opaque JSON is
self-consistent. Grant, policy and profile revisions; proposal bodies and actions;
condition evidence; placement explanations; and supervisor action sources are
immutable. Canonical hashes are checked against typed rows on read and before
acceptance or execution.

An accepted proposal requires one local-owner decision/application receipt. A
scheduled run requires one exact supervisor action and scheduling receipt; an
orphan job is not claimable. Projection, event, receipt, and idempotency writes
commit together and are exercised at named failure barriers. State reads and
`supervisor explain` reject detached hashes, events, scopes, profiles, or receipts.

These constraints are corruption barriers, not a separate authentication system.
A process with arbitrary write access to both the database and node key can forge
an entire internally consistent history and is outside the same-UID threat model.

### Executable acceptance matrix

The following IDs are stable requirements, not illustrative examples. Provider-
free scenario and store tests cite them so a surface-only success cannot mask a
transaction, authority, or recovery failure.

| ID | Adversarial setup | Required observable result |
| --- | --- | --- |
| `M16-AUTH-01` | Two enabled agents have the same arbitrary role label; only one exact planning task/assignment/profile/run has a current grant | Only the exact packet-v5 run advertises and can call its granted proposal tools; the other packet-v4 run is denied |
| `M16-AUTH-02` | A grantee calls the reserved acceptance tool or selects scope in tool input | The call is denied and audited; no owner decision or work row changes |
| `M16-AUTH-03` | The owner revokes the grant after packet-v5 launch and the run calls again | The historical packet remains readable, but current call authorization fails |
| `M16-PROP-01` | A valid typed proposal is submitted | Only proposal/action/audit/idempotency facts exist before acceptance |
| `M16-PROP-02` | One owner accepts `A -> B -> independent review`, then replays | One atomic effect set exists; replay returns it and creates no duplicate task, edge, requirement, intent, event, or decision |
| `M16-PROP-03` | Proposed actions form a cycle or cite another project/objective | Acceptance rejects the whole batch with no partial effects |
| `M16-PROP-04` | Proposed budget exceeds a finite envelope or uses unlimited under a finite envelope | Acceptance rejects the whole batch; zero is never treated as zero consumption |
| `M16-PROP-05` | A profile is absent from the grant, retired, stale, disabled, wrong-project, or bound to another agent revision | Submission or acceptance fails closed before assignment or intent creation |
| `M16-PROP-06` | A claim kind is ungranted, malformed, duplicate, or conflicts under deny/pause/resolution policy | Validation rejects it or scheduling defers it exactly as policy states; no partial lease is acquired |
| `M16-SUP-01` | Accepted B becomes dependency-ready after A completes | One exact action, assignment, context, run, job, and receipt are committed and B is scheduled once |
| `M16-SUP-02` | Concurrent scans contend for global, project, provider, agent, checkout, or claim capacity | The transactionally committed counts never exceed any limit; losing candidates remain explained and unscheduled |
| `M16-SUP-03` | A task/claim lease expires while its run is requested, starting, active, blocked, stopping, or lost | Runtime/job reconciliation happens first; the assignment, claim, and capacity are not released or reassigned |
| `M16-SUP-04` | The same blocked, stale, failed, wall-time over-budget, or repeated-failure condition is scanned repeatedly | Exactly one inert action and one approval request exist per condition key until one current owner decision |
| `M16-SUP-05` | A definite `start_failed` run is retried | Retry occurs only when the exact policy enables it, within limit and cooldown, as a new receipted run through the same immutable profile and still-unexpired assignment/claims |
| `M16-SUP-06` | The journal has more than 1,000 known facts, restart occurs between pages, or the next fact has an unknown event type | Known pages advance without effects and work is evaluated only after the captured cutoff; restart resumes exactly, while an unknown fact returns `unsupported_supervisor_event` with cursor and effects unchanged |
| `M16-SUP-07` | One accepted manager escalation is scanned, allowed/denied/replayed, or its exact target changes before allow | Exactly one `manager_escalation` action/approval freezes the proposal/action source; deny and replay have no second effect, allow applies only the still-current closed response, and stale targets fail closed |
| `M16-REC-01` | Failure is injected before and after proposal projection, event, decision, effects, intent, run, job, receipt, and idempotency barriers | Each operation is wholly absent or wholly committed and replayable; no orphan projection is executable |
| `M16-REC-02` | The daemon stops after durable intent/run/job commit but before worker launch, then restarts | Recovery claims the original job and run operation once; it does not create another scheduling action or run |
| `M16-SQL-01` | Direct SQL attempts to forge, detach, update, or delete authority/action/receipt rows | Constraints or read validation reject the state before acceptance, explanation, or worker claim |
| `M16-SQL-02` | A supervisor-origin job lacks an exact applied action and scheduling receipt matching run, assignment, task/profile/policy revisions, and scope | The worker refuses to claim or launch it |

## Consequences

- A manager can decompose an objective without gaining owner authority or
  reserving work merely by speaking.
- The owner approves the plan once; routine ready work can then advance without a
  human click while every placement remains explainable.
- Packet v5 adds explicit delegated authority without broadening historical v1-v4
  runs.
- Owner-defined launch profiles make automatic execution reproducible and prevent
  model-selected commands or providers.
- Keeping `lost` capacity reserved may intentionally halt scheduling, but it
  prevents two writers from being treated as one safe slot.
- Conservative approval defaults produce more owner requests for failure paths;
  policy can expand only through explicit, revisioned owner configuration.
- Exact typed rows, immutable receipts, and stable operation IDs make crash,
  contention, restart, raw-corruption, and idempotency behavior executable in
  provider-free tests.
- Model-assisted prioritization, autonomous plan acceptance, token/cost inference
  from terminal text, arbitrary workflow expressions, cross-project dependencies,
  Git integration, merge/push/deploy, and communication with people remain
  outside this milestone.

## Rejected alternatives

- Grant authority from `agent.role == manager`, or any other magic role string:
  labels describe work and are not capabilities.
- Put manager tools into packet v4: this would silently broaden historical run
  authority and invalidate its exact policy contract.
- Let a manager provide a scenario or executable: model output cannot define the
  local process boundary.
- Apply a proposal while it is submitted: proposal authorship and acceptance must
  remain independently attributable.
- Turn proposed claims into leases: inert recommendations must not reserve or
  block unrelated work.
- Treat zero budget as zero use: existing public semantics define zero as
  unlimited.
- Expire an assignment before inspecting its run: an expired lease is not proof
  that a process stopped writing.
- Exclude `lost` from capacity: uncertainty cannot safely free a writer slot.
- Launch inside the scheduling transaction: external process effects cannot be
  rolled back with SQLite.
- Trust a self-hashed JSON explanation: hashes detect byte changes but do not
  prove that embedded authority matches canonical rows.
