# ADR-0016: Owner-granted local checks as fresh mechanical evidence

- Status: accepted
- Date: 2026-08-13

## Context

Crewfold can schedule and recover agent runs, but it cannot yet execute a local
check as a distinct domain operation or retain its result as structured evidence.
An agent may self-report that a test passed, and the owner may inspect terminal
output, but neither is a durable mechanical-check record tied to an exact task
criterion and repository state.

Adding checks crosses several existing authority boundaries. An executable check
is an external process effect and must survive daemon failure without being run
twice. A passing process is not task completion, policy acceptance, merge
authority, or permission to change source. A useful check watcher may be any
owner-chosen agent, so a role label such as `CI watcher`, `reviewer`, or
`integrator` cannot authorize it. Results must reach the current task owner and
explicit evidence or coordination recipients without making the check subsystem
impersonate the local owner or an agent run. Finally, a result that verified one
commit cannot continue to describe a different or dirty working tree as verified.

The safe boundary is therefore an owner-authored command allowlist, an exact
project-scoped watcher grant, a separate restart-safe check lifecycle, explicit
criterion evidence and freshness, and deterministic routing. Remote CI,
worktree-content fingerprints, sandboxing, and integration policy remain separate
future concerns.

## Decision

### Descriptive role and purpose fields never grant check authority

`AgentDefinition.Role` and `LaunchProfile.Purpose` are descriptive metadata only.
They are never read to authorize a check tool, choose a watcher, select a route,
accept evidence, propose repair work, or decide what may run. Two agents with the
same arbitrary role string may hold different grants, and an agent with any role
string may receive the exact owner grant.

The local owner creates, revokes, and revises check definitions, task check
requirements, watcher grants, routes, policy, and repair decisions. A check
watcher receives only the closed operations in its current grant. The deterministic
check worker performs only a previously receipted definition. Neither may complete
a task, accept an outcome, push, merge, deploy, or decide integration order.

### The current packet optionally carries one exact check-watch grant

A `CheckWatchGrant` belongs to one workspace and project and binds one exact
enabled agent revision. Immutable child rows name its exact active check-definition
revisions and closed operations:

- `run`;
- `inspect`; and
- `propose_repair`.

The grant also fixes bounded pending and in-flight limits, expiry, lifecycle
revision, and a canonical content hash. It is project-scoped so an explicitly
appointed watcher can observe requirements belonging to tasks other than its own
coordination task. Every requested requirement must still be an active exact
project requirement whose definition revision appears in the grant.

The current context packet carries an optional complete `check_watch_grant`
snapshot. A packet without that grant stays on the same schema and receives no
watcher authority. One packet cannot contain both `check_watch_grant` and
`management_grant`; a run carries at most one delegated-authority family. The
same durable agent may use separate runs when the owner wants both workflows.

Binding and every MCP call revalidate the current enabled agent revision, live
bound run, packet, grant revision, expiry, project, requested operation, and exact
definition revision. Revocation denies new calls, including idempotent mutation
replays, without rewriting frozen packets. It does not cancel or broaden the
authority of an exact check launch receipt already committed for crash recovery.

Run-scoped MCP derives workspace, project, watcher agent, watcher run, packet, and
grant from the capability. The closed watcher tools are:

- `crewfold_run_check`, with only `requirement_id` and `idempotency_key`;
- `crewfold_list_check_results`, with only bounded pagination;
- `crewfold_inspect_check_result`, with only `check_run_id`; and
- `crewfold_propose_check_repair`, with only `check_result_id`, bounded rationale,
  and `idempotency_key`.

Tool discovery includes only the operations frozen in the current packet's exact
grant. No watcher tool
accepts a workspace, project, actor, checkout, executable, arguments, environment,
profile, grant, evidence class, or recipient. A reserved repair-acceptance name is
recognized only to return and audit policy denial.

### Definitions and named task requirements are owner-authored

A `CheckDefinition` is one project-local immutable command revision. It contains a
bounded name, an absolute executable, at most 64 ordered fixed arguments, a
normalized checkout-relative working directory, a timeout from 100 milliseconds
through one hour, a per-stream output limit from 1 KiB through 1 MiB, lifecycle,
and a canonical content hash.

It is not a shell command string. A definition cannot supply stdin, environment
variables, credential references, provider configuration, MCP access, or
caller-selected arguments. Retirement blocks future requests but does not alter
historical results or invalidate recovery of an already receipted child.

A `TaskCheckRequirement` is the minimal first-class acceptance criterion for M17.
It binds one task and project, a unique active criterion key and statement, and
one exact definition content revision. At most one active requirement for the
same task may name a definition. Consequently `check run unit --task TASK_ID`
resolves exactly one named criterion; one passing check can never fan out across
several criteria.

The owner may select an exact checkout explicitly. Otherwise Crewfold resolves the
currently reserved task run's checkout, then the latest task run's checkout in a
stable order, and fails when neither exists. Agent MCP has no checkout selector.
The request freezes the selected checkout and revision.

### Check execution has its own state machine

Check runs never occupy the agent `runs` table, create an agent assignment or
provider binding, consume agent-run capacity, or update task lifecycle.

```text
requested -> starting -> running -> finished
```

Every finished check has exactly one immutable result. Its closed outcome is:

- `passed` for a trusted normal exit with code zero;
- `failed` for a trusted nonzero or signal result;
- `timed_out` for a runtime-enforced timeout;
- `start_failed` when the child definitely did not start; or
- `unknown` when process identity or outcome cannot be trusted.

The request transaction freezes the requirement, definition, checkout, source
actor, job, event, and idempotent response. Before an external effect, the worker
inspects Git and commits a launch receipt that fixes the effective command digest,
checkout, source observation, and stable operation ID. The check-run ID is also
the direct-runtime operation ID.

Only one `requested`, `starting`, or `running` check may exist for a requirement
and checkout. An explicit rerun with a new idempotency key is allowed after the
previous run finishes.

The check worker uses `RuntimeStatusInspector.InspectStatus` for lifecycle, exit,
forced-stop, and diagnostic fields without output. It reads text only through the
runtime driver's bounded, redacted `Logs` method. It never consumes or persists
raw captures from `RuntimeSnapshot.Stdout` or `RuntimeSnapshot.Stderr`.

The direct runtime compares a canonical effective-spec digest on replay. The
digest covers the exact executable, ordered arguments, empty stdin,
check-specific allowlisted environment, absolute working directory, clamped
output limit, timeout, grace period, schema, and operation ID. A mismatch is a
conflict rather than a successful replay.

The dedicated check direct-runtime instance has a separate state subtree. It
inherits only `PATH`, locale variables, `TMPDIR`, and `TZ`, and adds
`CREWFOLD_CHECK_RUN_ID` as non-authoritative diagnostic metadata. It does not
inherit provider configuration, `CODEX_HOME`, `CLAUDE_CONFIG_DIR`, the MCP socket,
or a capability file. The effective working directory must resolve beneath the
real checkout without a symlink escape.

Direct execution is not a security sandbox and does not promise to block network
or Git access from an owner-authored executable. The owner is responsible for the
definitions they allowlist. Crewfold guarantees that a grantee cannot alter that
definition and that result handling itself has no Git, deployment, or integration
effect.

### Recovery records one result or an explicit unknown

A crash before the launch receipt leaves no child authority. A crash after that
receipt but before launch leaves the original durable job claimable. A crash after
launch but before handle persistence replays the same operation with the same
sealed spec and recovers its binding. A temporary adapter outage defers the job
without inventing completion.

A trusted terminal observation finalizes exactly once. If the runtime supervisor
or child identity cannot prove an outcome, Crewfold records one `unknown` result
and never silently starts another child. A later definition retirement or grant
revocation prevents future requests but does not turn recovery of that exact
receipted operation into a second authorization decision.

Redacted stdout, stderr, and a structured diagnostic are bounded artifacts.
Artifact blobs are content-addressed beneath the private data directory; immutable
metadata stores hash, captured bytes, omitted bytes, and truncation. A blob may be
written before the terminal database transaction and become an unreferenced
orphan after a crash, but it has no authority. A committed result cannot reference
missing or hash-mismatched content.

### Result outcome, source validity, and current freshness are separate

Every result records repository, object format, checkout, branch, launch HEAD,
launch dirty state and sorted dirty paths, and an equivalent terminal observation.
The process outcome says what the command did. It does not by itself say that the
result verifies the current task source.

A result is initially verification-eligible only when both Git observations are
available, refer to the exact same nonempty HEAD, repository, and checkout, and
both working trees are clean:

- equal clean HEAD observations produce `fresh`;
- different known HEAD observations produce `stale`; and
- dirty, unavailable, invalid, or incomplete observations produce `unknown`.

Thus a check may run against dirty source for diagnostics, but even exit zero
cannot satisfy a criterion until a clean-HEAD run is recorded.

The watch reconciler obtains a new real `GitInspector` observation rather than
trusting the cached checkout `observed_at`. A different HEAD or any dirty working
tree marks a prior result stale. Staleness is monotonic: returning to an older HEAD
or cleaning the worktree never resurrects the result. A temporary inspection
failure may project `unknown` and later return to `fresh` only when the result was
originally verification-eligible and no stale observation has ever occurred.
Every observation retains its timestamp and reason.

M17 freshness is deliberately commit/HEAD freshness rather than a dirty-content
fingerprint. A transient HEAD movement that no observer sees is outside its
guarantee. Clean launch/terminal boundaries, periodic observation, conservative
dirty invalidation, and explicit timestamps make that limitation visible.

For each active requirement, the derived state is one of `missing`, `running`,
`verified`, `failed`, `stale`, or `unknown`. Only the latest exact-revision result
that is both passed and fresh is verified. Missing, stale, and unknown evidence
remain visible rather than being summarized as success.

### Mechanical evidence satisfies only the named criterion

Evidence classification is a closed vocabulary:

- `agent_self_report`;
- `mechanical_check`;
- `independent_review`; and
- `policy_acceptance`.

A check-result evidence link is database-forced to `mechanical_check`; neither an
owner client nor an agent can select or upgrade it. A fresh pass supports only the
one requirement frozen into the run. A nonpass result attaches contradicting or
inconclusive evidence. Check evidence never changes task state, accepts completion,
or creates policy acceptance. Inspection exposes all four categories, including
empty ones, so absence cannot be collapsed into confidence.

### Routes bind exact duties and notifications have subsystem provenance

Every nonpass result attempts a mandatory route to the current active task
assignment. The notification receipt freezes the assignment ID and revision plus
the exact recipient agent revision. If no current task owner exists, Crewfold
records an `unroutable` fact and does not guess a former assignment or match a
role/name.

The owner may add exact project and optional-definition routes for `pass`,
`nonpass`, or `stale`. Each route binds one exact agent revision and one explicit
`evidence_review` or `coordination` duty. The route itself is the owner assignment
of that duty; no role label supplies it.

Check notifications use the existing inbox and wake machinery without
impersonation. `messages.sender_type` adds `subsystem`, with the exact sender
`crewfold-check-worker`, no sender agent/run, a direct thread, and an immutable
one-to-one receipt proving the check result, policy/route, duty, recipient, and
task assignment when applicable. Agent and owner message paths cannot select
subsystem provenance.

### Repair proposals are policy-bounded and inert

Project check policy defaults repair proposals to disabled. When enabled, it
names an exact active repair launch-profile revision and a bounded open-proposal
limit. A watcher also needs the grant operation `propose_repair`.

The watcher supplies only the latest exact trusted failed result at the current
fresh source and bounded rationale. Timed-out, start-failed, and unknown outcomes
or stale/unknown freshness remain inspectable but cannot seed a proposal.
The resulting `CheckRepairProposal` freezes the authenticated source run, exact
agent revision, and exact grant revision; role and purpose remain irrelevant. It
does not create a task, assignment, scheduling
intent, dependency, claim, or task transition. The owner accepts or rejects it
with expected revision and idempotency. Acceptance revalidates that exact result,
project, source task/objective, current policy revision, and exact active repair
profile, then creates one linked repair task, scheduling intent, decision, effect
receipt, events, and idempotent response atomically. The watcher cannot choose an
agent, profile, command, or budget. A later fresh pass makes a pending proposal
stale. The immutable decision records `accepted|rejected`, the exact proposal
revision decided, an optional canonical note bounded to 4096 encoded UTF-8 bytes,
its timestamp, and exact `local-owner` authorship. Repair detail omits a decision
before it exists and exposes an effect only for acceptance.

### Watch passes are bounded and do not auto-launch missing work

`check watch --project PROJECT` performs one deterministic bounded pass. It
reconciles in-flight children, inspects freshness, routes pending notifications,
stales obsolete repair proposals, and returns a cursor and explanation. It does
not silently launch every missing requirement; the local owner or a current
granted watcher explicitly requests a run.

The daemon invokes the same reconciler in the background. Each pass considers at
most 100 candidates. A public idempotent pass stores a receipt and emits
`check.watch_completed` even when it has no effect; a background exact no-op
writes no receipt or event. Cursor processing classifies a closed event union and
never acts across an unknown fact. The public completion fact proves that the
bounded owner request committed, not that it appended a freshness revision; the
receipt counters state the latter exactly.

### Local API, CLI, MCP, and storage remain explicit

The owner-local API remains protocol version 1 and adds strict, versioned methods
for:

- `check.definition.create|retire|show|list`;
- `check.requirement.create|retire|list`;
- `check.grant.create|revoke|show|list`;
- `check.route.create|retire|list`;
- `check.policy.show|configure`;
- `check.run|list|inspect|logs|watch`; and
- `check.repair.list|inspect|accept|reject`.

Mutations require idempotency keys. Lifecycle and decision operations require an
expected revision. The CLI mirrors these methods, including
`check run NAME --task TASK_ID [--checkout CHECKOUT]`,
`check watch --project PROJECT`, `check inspect CHECK_RUN_ID`, `check logs`, setup,
grant, route, policy, repair, and `--output json`.

The single current baseline defines:

1. `check_definitions`;
2. `check_definition_arguments`;
3. `task_check_requirements`;
4. `check_watch_grants`;
5. `check_watch_grant_operations`;
6. `check_watch_grant_definitions`;
7. `check_policies`;
8. `check_routes`;
9. `check_runs`;
10. `check_jobs`;
11. `check_launch_receipts`;
12. `check_results`;
13. `check_artifacts`;
14. `check_result_freshness`;
15. `check_requirement_evidence`;
16. `check_notification_receipts`;
17. `check_route_failures`;
18. `check_repair_proposals`;
19. `check_repair_decisions`; and
20. `check_repair_effects`.

The single current baseline defines subsystem message provenance together with
all participant/immutability triggers, validates the optional check-watch grant in
the current context packet, and seeds every project with a safe disabled repair
policy.

Authority-significant IDs, scope, revisions, command fields, state, source
observations, routes, recipients, results, and receipts are typed columns or
immutable child rows rather than self-consistent JSON alone. Trigger families
validate exact scope and canonical mirrors, restrict lifecycle transitions, keep
child ordinals contiguous, reject update/delete of history, require one launch
receipt before a job can execute, require one terminal result, constrain legal
freshness transitions, force mechanical evidence, prove subsystem notification
sources, and keep repair proposals inert until one owner decision.

New persistence uses named sqlc queries and generated models. The bounded
handwritten M16 exception does not extend to M17.

### Idempotency and transaction barriers fail closed

Idempotency keys are actor-scoped. The same key and canonical request returns the
frozen response; different content conflicts. Agent mutation replay first
revalidates the current packet's exact check-watch grant.

Uniqueness enforces one job, launch receipt, and result per check run; one artifact
of each kind; contiguous freshness revisions; and one immutable mechanical
evidence link per requirement/result/freshness revision. The initial revision-1
link remains historical evidence, while a later stale revision gets its own
inconclusive link instead of rewriting or hiding the earlier truth. There is also
one notification per result or freshness revision, route, duty, and recipient;
one repair proposal per result/policy revision; and one repair decision/effect.

Named failure barriers surround the request projection/event/job/idempotency,
launch receipt/event, external launch, handle binding, terminal
result/artifact/freshness/evidence/notification/message/event bundle, and repair
decision/effect. Terminal database effects commit together. An orphan job is not
claimable, a detached result is not readable as valid evidence, and an existing
operation with a different launch digest is never accepted as recovery.

Check event types are added to the M16 supervisor's closed journal classifier and
the live-context cursor classifiers. A binary never advances an automation cursor
past an unknown event.

The check-run lifecycle fact set distinguishes `check.run_starting` (immutable
pre-effect launch receipt), `check.run_runtime_observed` (stable direct-runtime
handle recorded after launch or exact replay), `check.run_started` (running
transition), and `check.run_finished` (one terminal result). This separation makes
the crash windows inspectable without treating a receipt or a recovered binding
as proof that the child is already running.

### Executable acceptance matrix

These IDs and results are stable requirements. Provider-free store, daemon, CLI,
runtime, protocol, raw-SQL, fault-injection, and black-box fixture tests cite them.

| ID | Adversarial setup | Required observable result |
| --- | --- | --- |
| `M17-AUTH-01` | same arbitrary Role on two agents, one exact grant | only the exact granted run advertises/calls tools |
| `M17-AUTH-02` | Role/Purpose renamed to watcher/manager/integrator | authority/routing unchanged; no query checks those fields |
| `M17-AUTH-03` | Current packet without a grant, revoked/expired/stale grant, wrong project/definition/revision, scope/command/checkout injection | denied+audited, no check rows/effect |
| `M17-AUTH-04` | Check-watch and manager grants coexist; reserved repair acceptance called | packet is invalid; reserved operation is denied |
| `M17-DEF-01` | owner definition freezes exact executable/argv/workdir/timeout/cap; agent attempts env/stdin/shell-arg injection; definition then retires | injection is impossible or denied; retirement blocks new run but not receipted recovery |
| `M17-RUN-01` | clean HEAD pass | exactly named criterion becomes `verified`; no other criterion/task status changes |
| `M17-RUN-02` | trusted nonzero/signal | failed result+mechanical evidence and exactly correct current task-owner subsystem message |
| `M17-RUN-03` | timeout/excess output/crash | one outcome plus bounded redacted stdout/stderr/diagnostic artifact with omitted counts/hash |
| `M17-EVID-01` | caller/raw SQL attempts to label check as independent/policy acceptance or make stale/missing verified | rejected/read fails closed |
| `M17-FRESH-01` | HEAD change or dirty observation | one monotonic stale revision; return to old HEAD does not revive; rerun needed |
| `M17-FRESH-02` | dirty-boundary/unavailable/missing result | visible unknown/missing and never verified, even exit 0 |
| `M17-ROUTE-01` | owner/evidence/coordination recipients have arbitrary identical role strings | exact assignment/route receipts select only intended agents; no active owner records unroutable |
| `M17-MSG-01` | raw/agent attempt to forge subsystem/owner provenance or detach result/recipient | constraint/read rejection |
| `M17-REPAIR-01` | failure with disabled/no-op grant; then enabled exact policy+granted op; owner replay or later fresh pass | disabled path creates no proposal; enabled path creates one inert proposal; only owner accept creates one exact-profile repair task/intent; replay/stale pass creates no duplicate |
| `M17-REC-01` | inject at every DB barrier | wholly absent or wholly committed/replayable; orphan job/result/message/evidence is never executable/read as valid |
| `M17-REC-02` | daemon stop while child runs | stable spec/ID reconciles same child and records exactly one result, or explicit unknown, never second execution |
| `M17-REC-03` | tamper launch.json/effective replay spec/handle/output blob | conflict or unknown/read failure, never trusted pass |
| `M17-WATCH-01` | >100 facts/restart/unknown event | cursor pages restart-safely, no action across unknown; background no-op emits nothing, public no-op replay returns one receipt and its completion event without implying freshness changed |
| `M17-ENV-01` | inspect check child environment and output paths | process lacks provider/MCP secret env; status API has no captures; only Logs output is persisted and redacted/bounded |
| `M17-GIT-01` | pass/fail/result/repair handling completes | no commit/push/merge/deploy/integration-order effect |
| `M17-SQL-01` | raw SQL update/delete/detach/forge of definition/grant/receipt/result/freshness/evidence/route/repair rows | rejected or canonical read fails |
| `M17-SQL-02` | job lacks matching exact launch authority/event/receipt; later definition/grant retires after valid receipt | orphan worker claim is refused; exact committed recovery remains valid |

## Consequences

- Any explicitly chosen agent can watch checks without a built-in watcher role or
  merge authority.
- A passing process becomes inspectable mechanical evidence for one named
  criterion, while task completion and policy acceptance remain separate.
- Clean-HEAD verification is conservative. Dirty work remains useful for failure
  diagnosis, but a passing dirty run cannot be presented as verified.
- Restart-safe receipts and sealed runtime replay prevent daemon crashes from
  turning one request into two check children.
- Existing inbox delivery can carry check failures with honest subsystem
  provenance and exact route receipts.
- Repair work remains owner-decidable and profile-bounded rather than a hidden
  effect of a failing command.
- A project may require another explicit run after a transient Git change or
  conservative stale observation. That cost is preferable to resurrecting
  invalid evidence.
- Owner-authored checks can still access the local machine and network. Strong
  sandboxing, no-network enforcement, toolchain attestation, dirty-content
  fingerprints, and remote CI are deferred rather than implied.

## Rejected alternatives

- Authorize `role == "CI watcher"`, `role == "reviewer"`, or profile purpose:
  descriptive strings are not capabilities.
- Put check tools into every current packet: this silently broadens ordinary or
  manager agents without an exact grant and mixes delegation families.
- Store checks as agent runs: runtime completion, agent assignment, task state,
  and mechanical evidence have different lifecycles and authorities.
- Let a watcher supply a command, arguments, environment, checkout, or recipient:
  the owner allowlist and exact route are the security boundary.
- Treat exit zero as task completion or policy acceptance: a check proves only its
  named criterion at its recorded source freshness.
- Treat same HEAD on a dirty tree as verified: M17 has no dirty-content
  fingerprint and must not overclaim.
- Restore freshness when HEAD later returns: observed staleness is durable
  evidence that a rerun is required.
- Send failure mail as `local-owner` or a fabricated agent run: subsystem
  provenance must remain attributable.
- Create a repair task at proposal time: a watcher supplies evidence and rationale,
  not owner task-creation authority.
- Relaunch after an unknown runtime outcome: uncertainty cannot safely authorize a
  second possible process.
- Claim that direct execution is a sandbox: the trusted owner-authored local
  executable boundary is explicit.
