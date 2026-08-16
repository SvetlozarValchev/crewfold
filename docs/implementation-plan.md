# Implementation plan

## Purpose

Crewfold will be built as a sequence of small vertical slices. Every milestone must
leave behind a runnable product increment, not only internal abstractions. A
milestone is complete only when its happy path, important failure path, persistence
behavior, and operator-visible result can be demonstrated independently.

The detailed commands below describe acceptance contracts. They may change before
implementation begins, but each replacement must remain equally observable and
testable.

## Delivery contract

Every milestone must provide all of the following:

1. **Visible behavior:** a human can invoke a command and see the new capability.
2. **Deterministic demo:** a checked-in scenario runs from a clean temporary home
   without paid providers or network access.
3. **Automated acceptance:** the demo's assertions run in CI.
4. **Failure proof:** at least one representative failure is injected and produces
   a useful, stable diagnosis.
5. **Persistence proof:** if the milestone writes durable state, restart behavior
   is tested.
6. **Inspectable state:** structured output, logs, operation IDs, and relevant
   events identify where a failure occurred.
7. **Bounded scope:** deferred behavior is listed and cannot be smuggled into the
   milestone's definition of done.
8. **Documentation:** user-facing commands and architectural consequences are
   updated in the same change.
9. **Outcome legibility:** the milestone states whether it changes a commitment,
   accepted outcome, verification basis, active risk, or owner decision, so later
   management projections do not have to infer meaning from activity logs.

A milestone does not pass because its packages compile or because a unit test mocks
away every boundary.

### Size guardrails

A milestone should prove one user-observable capability and introduce at most one
new unreliable external boundary. If it cannot be demonstrated in a few minutes,
needs several unrelated subsystem rewrites, or is likely to remain open across many
weeks of normal development, split it before implementation. A milestone may span
several small commits, but every commit must keep the existing suite green.

## Test surfaces from the beginning

The implementation must support these controls before real agents are involved:

```text
--data-dir <temporary-directory>
--socket <explicit-path>
--output json
--log-level debug
--runtime fake|direct|herdr
--provider fake|generic|codex|claude
--clock real|controlled-test-clock
```

Tests also need deterministic IDs and time through internal interfaces. Production
defaults use secure IDs and a real clock; test injection must never leak into the
normal configuration accidentally.

Each durable command returns:

- an operation or command ID;
- the affected entity ID and revision;
- the committed event cursor;
- whether an asynchronous effect is pending, complete, or failed.

That output is the first diagnostic boundary when a test fails.

## Stage A — Prove the local kernel

### M0 — Buildable repository

**Question answered:** Can a contributor build, test, and inspect one Crewfold
binary from a clean checkout?

**Visible result**

```sh
crewfold version
crewfold help
crewfold doctor --self
```

**Deliverables**

- Go module and `cmd/crewfold` entry point.
- Version package with development-build metadata.
- Consistent error envelope and exit-code conventions.
- Formatting, vet/static analysis, and unit-test commands.
- Local CI entry point that runs the same checks as future hosted CI.
- Build metadata does not require Git or network access at runtime.

**Automated acceptance**

- Build from a clean module cache where dependencies are already available.
- `version --output json` validates against its schema.
- Unknown commands and malformed flags return documented non-zero exit codes.
- Unit tests run with the race detector where supported.

**Failure injection**

- Invoke an unknown command and verify a concise error, help hint, and no stack
  trace unless debug output is requested.

**Exit gate**

A single documented command runs all M0 checks locally. There is still no daemon,
database, runtime, or provider code.

### M1 — Daemon and local API spine

**Question answered:** Can one process expose a healthy, observable local control
plane and shut down cleanly?

**Visible result**

```sh
crewfold daemon run --data-dir "$tmp" --socket "$tmp/crewfold.sock"
crewfold status --socket "$tmp/crewfold.sock"
crewfold daemon stop --socket "$tmp/crewfold.sock"
```

**Deliverables**

- Foreground daemon with user-only Unix socket permissions.
- Request/response envelope, health query, and exact current protocol selection.
- Structured logs with request/operation correlation IDs.
- Graceful shutdown and stale-socket handling.
- CLI client that can use an explicit socket and emit JSON.

**Automated acceptance**

- Black-box test starts the real binary, waits for readiness, queries status, and
  stops it.
- A second daemon cannot silently claim the same socket/data directory.
- Socket permissions are owner-only on supported Unix systems.
- Shutdown completes with an in-flight read request.

**Failure injection**

- Start against an occupied socket and verify that the error distinguishes a live
  daemon from a stale socket.

**Exit gate**

The process lifecycle smoke test is deterministic and leaves no daemon or socket
behind. Storage is still in memory.

### M2 — Persistent workspace and event journal

**Question answered:** Can Crewfold commit, inspect, restart, and recover its first
durable domain state?

**Visible result**

```sh
crewfold daemon run --data-dir "$tmp/data" --socket "$tmp/crewfold.sock"
crewfold workspace init personal --socket "$tmp/crewfold.sock" \
  --idempotency-key initialize-personal
crewfold workspace show personal --socket "$tmp/crewfold.sock"
crewfold events list --socket "$tmp/crewfold.sock" --after 0
crewfold daemon stop --socket "$tmp/crewfold.sock"
# Restart the foreground daemon with the same data/socket paths, then:
crewfold workspace show personal --socket "$tmp/crewfold.sock"
```

**Deliverables**

- SQLite connection, WAL configuration, foreign keys, and the embedded current schema baseline.
- Workspace table, event journal, idempotency records, and schema-baseline metadata.
- Atomic command handling: state projection and event append commit together.
- Database status and schema version in `doctor`.

**Automated acceptance**

- Workspace survives daemon restart with the same ID and revision.
- Repeating a command with one idempotency key returns the original result and does
  not append a second event.
- Failed invariant checks append no event and mutate no projection.
- A fresh database exactly matches the checked-in current baseline and rejects
  partial, foreign, or canonically invalid state.

**Failure injection**

- Kill the daemon after command receipt at controlled transaction checkpoints and
  prove the command is either wholly committed or absent.

**Exit gate**

The repository has a crash/restart persistence test and an inspectable event
history. No project or agent model exists yet.

## Stage B — Prove one complete agent-work loop

### M3 — Projects, repositories, and checkouts

**Question answered:** Can Crewfold safely register and observe real local source
locations without mutating them?

**Visible result**

```sh
crewfold project add demo --repo /tmp/demo-repo
crewfold checkout list demo
crewfold project inspect demo
```

**Deliverables**

- Project, repository, and checkout records.
- Git repository identity, branch, HEAD, dirty state, and worktree observation.
- Checkout write modes: `exclusive`, `claimed`, `shared`, and `read_only`.
- Path normalization, missing-path diagnosis, and duplicate-checkout detection.
- Deterministic fixture repository generator for tests.

**Automated acceptance**

- Register a clean fixture Git repository, modify it, and observe the new status.
- Register two worktrees of one repository and preserve distinct checkout IDs.
- Reject a non-repository path without creating partial records.
- Move or remove a registered path and report it as unavailable without deleting
  its identity.

**Failure injection**

- Make Git unavailable or return malformed output through a fake command runner;
  verify a scoped diagnostic and unchanged durable registration.

**Exit gate**

Crewfold can explain what it knows and does not know about a real checkout, and the
test proves registration performs no source mutation.

### M4 — Durable agents and tasks

**Question answered:** Can the user describe who should work and what should be
done before any process is launched?

**Visible result**

```sh
crewfold agent create implementer --workspace personal --role implementer \
  --provider fake --socket /path/to/crewfold.sock
crewfold task create --workspace personal --project demo --title "Add greeting" \
  --socket /path/to/crewfold.sock
crewfold task assign TASK_ID implementer --workspace personal \
  --lease-seconds 3600 --expected-revision 1 --socket /path/to/crewfold.sock
crewfold status --workspace personal --socket /path/to/crewfold.sock
```

**Deliverables**

- Agent definitions, objectives, tasks, dependencies, assignment leases, and
  budgets.
- State-machine validation and optimistic revision checks.
- CLI create/list/show/update/assign operations.
- Status projection that distinguishes registered, assigned, active, and blocked.
- Deterministic ready-task query with a human-readable explanation.

**Automated acceptance**

- Create, assign, block, unblock, and cancel tasks through the real local API.
- Reject circular dependencies and double primary assignment.
- Expire an assignment using a controlled clock without deleting task history.
- Restart the daemon and preserve every state and event.

**Failure injection**

- Submit two updates at the same expected revision; exactly one succeeds and the
  other receives a revision-conflict diagnosis.

**Exit gate**

The owner can model work and inspect readiness without running agents. Runtime and
provider fields remain capability/configuration data, not core branches.

### M5 — Deterministic fake-agent vertical slice

**Question answered:** Does the complete task-to-run-to-handoff state machine work
without a real process or model obscuring failures?

**Visible result**

```sh
crewfold run start TASK_ID --workspace personal --runtime fake --provider fake \
  --scenario ./scenario.json --expected-task-revision 2 --socket ./crewfold.sock
crewfold run watch RUN_ID --workspace personal --socket ./crewfold.sock
crewfold task timeline TASK_ID --workspace personal --socket ./crewfold.sock
```

**Deliverables**

- Runtime-driver and provider-adapter interfaces.
- Fake runtime/provider controlled by a scenario file.
- Scheduler placement across task, agent, checkout, and concurrency constraints.
- Durable run intents and asynchronous worker queue.
- Progress, blocked, completion proposal, acceptance, and handoff records.
- Run/task timeline in CLI output.

**Automated acceptance**

- Scenario A progresses and completes with evidence and handoff.
- Scenario B blocks on a question and remains resumable.
- Scenario C fails during start and releases reserved capacity.
- Scenario D reports completion but fails an acceptance rule, leaving the task in
  review or changes-requested state.
- Daemon restart with requested intent, after launch/before acknowledgement,
  while blocked, and at an active checkpoint reconciles to one correct result
  without duplicate launches.

**Failure injection**

- Crash after `run.requested` but before `run.started`; restart and prove the
  operation is resumed or reconciled exactly once.

**Exit gate**

One checked-in acceptance scenario demonstrates the whole durable loop. This is
the first internal preview release and the foundation for every later runtime.

### M6 — Direct subprocess runtime

**Question answered:** Can Crewfold safely supervise a real local process while
keeping the deterministic domain behavior from M5?

**Visible result**

```sh
crewfold run start TASK_ID --runtime direct --provider fixture
crewfold run logs RUN_ID --tail 50
crewfold run stop RUN_ID --graceful
```

**Deliverables**

- Direct child-process launch with explicit working directory and environment.
- Bounded stdout/stderr capture, exit observation, graceful stop, and forced-stop
  fallback.
- Process/runtime handle persistence and orphan reconciliation.
- Fixture worker executable that speaks the test adapter protocol.
- Environment allowlist and secret-redaction tests.

**Automated acceptance**

- Fixture process completes, blocks, crashes, ignores graceful stop, and produces
  excessive output.
- Output remains bounded and an omitted section is reported.
- Daemon restart detects whether the child is alive, exited, or cannot be trusted.
- A process cannot escape its assigned checkout through a Crewfold path argument;
  operating-system sandboxing remains separately documented.

**Failure injection**

- Terminate the daemon while the fixture worker continues; restart and reconcile
  without pretending an unknown process result is success.

**Exit gate**

All M5 scenarios pass against both fake and direct drivers. No paid agent is
required.

### M7 — Run-scoped MCP and briefing

**Question answered:** Can an agent participate through a provider-neutral,
least-authority interface?

**Visible result**

```sh
crewfold context build --task TASK_ID --agent implementer
crewfold run start TASK_ID --provider fixture-mcp
crewfold context explain CONTEXT_PACKET_ID
```

**Deliverables**

- Run-scoped MCP endpoint and capability authentication.
- Immutable base context packet containing role, task, checkout, policy, and
  reporting instructions.
- MCP tools for briefing, status, progress, blocked, artifact, and completion.
- Tool-call audit events and concise structured errors.
- Fixture MCP client used by the direct worker.

**Automated acceptance**

- A run reads only its own briefing and reports progress/completion.
- A token for run A cannot read task/run B.
- Expired or stopped-run capability is rejected.
- Duplicate tool mutations are idempotent.
- Packet contents and exclusion explanations are stable under a controlled fixture.

**Failure injection**

- Fixture agent attempts cross-task access and receives `out_of_scope` while the
  attempt is safely audited.

**Exit gate**

The M5 end-to-end loop uses MCP rather than a test-only back door. Knowledge search
and inter-agent messaging remain deferred.

### M8 — Durable two-agent messaging

**Question answered:** Can two agents communicate through Crewfold even when one
is not currently running?

**Visible result**

```sh
crewfold message send reviewer --kind question --body "Check the public contract"
crewfold inbox --agent reviewer
crewfold run start --agent reviewer --task REVIEW_TASK
crewfold thread show THREAD_ID
```

**Deliverables**

- Messages, recipients, threads, delivery, read, and acknowledgement state.
- MCP inbox/read/send/acknowledge tools.
- Wake-up queue separated from durable message storage.
- Communication policy and bounded body/artifact behavior.
- Inbox summary added to new context packets.

**Automated acceptance**

- Agent A sends to stopped agent B; B later starts, reads, and acknowledges.
- Restart between send and delivery produces no duplicate message.
- Unauthorized broadcast or human-directed message is denied.
- A large payload must be published as an artifact rather than hidden in a message.

**Failure injection**

- Runtime wake-up fails while durable delivery succeeds; the status clearly shows
  “queued/unseen,” not “message lost.”

**Exit gate**

Two fixture agents complete a request/reply/handoff scenario using only their MCP
tools. This is the first genuinely multi-agent Crewfold release.

## Stage C — Prove real interactive agents without changing the core

### M9 — Herdr runtime driver with fixture agent

**Status:** complete on 2026-08-12. The implementation uses Herdr's documented
CLI/schema surface with a stable-terminal runtime handle, a provider-free pane
supervisor, deterministic recorded endpoint coverage, and an opt-in isolated live
session. See [the milestone review](reviews/herdr-runtime.md).

**Question answered:** Can Crewfold place and supervise an interactive run in Herdr
without provider-specific behavior confusing the result?

**Visible result**

```sh
crewfold doctor --runtime herdr
crewfold run start TASK_ID --runtime herdr --provider fixture-terminal
crewfold run attach RUN_ID
```

**Deliverables**

- Herdr probe and installed API-schema compatibility report.
- Workspace/tab/pane mapping, fixture-agent start, status observation, prompt,
  attach, interrupt, and stop.
- Stable mapping from Crewfold run to Herdr runtime handles.
- Reconciliation after Crewfold or Herdr restart.
- Live test suite that is opt-in when Herdr is installed.

**Automated acceptance**

- Fake Herdr protocol fixtures cover all normal/error responses in regular CI.
- Opt-in live test creates an isolated workspace, runs the fixture agent, attaches
  or observes, and cleans up only its own surface.
- Moving a Herdr pane does not change Crewfold task identity.
- Closing a pane produces a lost/failed observation, never automatic completion.

**Failure injection**

- Simulate an unsupported Herdr schema and verify `doctor` blocks launch with an
  actionable compatibility error.

**Exit gate**

M5 and M8 scenarios pass through Herdr using fixture agents. Provider integration
has not begun, so failures remain attributable to the runtime driver.

### M10 — Codex canary

**Status:** complete on 2026-08-12. Offline acceptance and the owner-authorized
real-model canary both pass. The adapter uses stable `codex exec --json`, a
required run-scoped STDIO MCP bridge, ephemeral provider history, and the existing
Herdr/direct runtime contracts. No user Codex configuration is modified and no
model call occurs in the default gate. On this host, an independently confined,
read-only container supplied the outer sandbox because host AppArmor policy blocks
Codex's nested bubblewrap namespace. See the
[passed milestone audit](reviews/codex-canary.md).

**Question answered:** Can one real Codex session execute the already-proven MCP
work loop in a disposable repository?

**Visible result**

```sh
crewfold doctor --provider codex
crewfold run start CANARY_TASK --runtime herdr --provider codex
crewfold run attach RUN_ID
```

**Deliverables**

- Codex capability/version probe and launch manifest.
- Run-scoped MCP configuration and initial context delivery.
- Normalized lifecycle observations and optional native resume metadata.
- Disposable canary repository and tiny task with an exact expected diff/test.
- Explicit live-test cost/network opt-in.

**Automated acceptance**

- Offline CI validates launch/config fixtures and adapter contracts.
- Opt-in live canary changes one allowed fixture file, runs one check, reports a
  handoff, and never pushes or accesses unrelated projects.
- Blocked/approval UI is represented as uncertain/blocked observation rather than
  completion.

**Failure injection**

- Remove MCP configuration or authentication and verify diagnosis identifies the
  provider/MCP boundary instead of timing out generically.

**Exit gate**

One real Codex run completes the same contract as the fixture agent. No Claude work
is included in this milestone.

### M11 — Claude Code canary and provider-neutral proof

**Status:** complete on 2026-08-12. The recorded Claude endpoint,
compatibility/failure probes, strict scoped launch, and Codex-to-Claude durable
handoff pass without credentials, network access, or inference. The installed
real-model canary remains an explicit opt-in conformance check for releases and
provider upgrades; it is not a development milestone gate. See the
[passed implementation audit](reviews/claude-canary.md).

**Question answered:** Does a second provider use the same Crewfold domain and MCP
tools without core changes?

**Visible result**

```sh
crewfold doctor --provider claude
crewfold run start CANARY_TASK --runtime herdr --provider claude
```

**Deliverables**

- Claude Code capability/version probe and launch manifest.
- MCP/context/lifecycle integration equivalent to the Codex contract.
- Side-by-side conformance report for fake, Codex, and Claude adapters.
- Any provider-specific metadata remains isolated in the adapter boundary.

**Automated acceptance**

- Offline contract suite passes unchanged for both provider adapters.
- The opt-in installed-Claude canary remains available for release/upgrade
  conformance but is not part of the deterministic completion gate.
- One scenario starts with Codex, records a handoff, and resumes with Claude in a
  new run without sharing provider-private transcript state.

**Failure injection**

- Feed a provider version outside the tested range and produce a capability warning
  or safe refusal according to declared compatibility.

**Exit gate**

The recorded endpoint and provider-switch scenario pass through the same public
domain and MCP contracts without provider-name conditionals in core policy.
Crewfold has demonstrated deterministic multi-provider portability; current live
provider behavior remains optional external conformance evidence.

## Stage D — Prove coordination intelligence

### M12 — Claims and deterministic overlap detection

**Status:** complete on 2026-08-12. Crewfold now stores leased path, component,
and operation claims; derives exact overlap witnesses and policy responses without
embeddings; watches dirty paths; and records restart-aware drift without changing
declared scope. The deterministic local acceptance uses adjacent clones and a
shared checkout. See [the milestone review](reviews/claims-overlap.md).

**Question answered:** Can Crewfold warn about conflicting intent before agents
discover each other through broken Git state?

**Visible result**

```sh
crewfold claim add TASK_A --workspace personal --project demo \
  --checkout CHECKOUT_A --write 'src/contact/**' --lease 1h --socket ./crewfold.sock
crewfold claim add TASK_B --workspace personal --project demo \
  --checkout CHECKOUT_B --write 'src/contact/cache.go' --lease 1h --socket ./crewfold.sock
crewfold overlap list --workspace personal --project demo --socket ./crewfold.sock
crewfold overlap inspect OVERLAP_ID --workspace personal --socket ./crewfold.sock
```

**Deliverables**

- Leased path/component/operation claims and conflict rules.
- Git watcher for HEAD, dirty paths, and claim drift observations.
- Deterministic overlap severity and explanation.
- Policy responses: notify, deny new claim, pause scheduling, or request resolution.
- Shared-checkout warnings without claiming filesystem isolation.

**Automated acceptance**

- Path/glob conflict matrix covers exclusive/shared/advisory modes.
- Observed write outside scope creates drift but does not rewrite the claim.
- Claim expiry and daemon restart are reconciled with a controlled clock.
- Separate worktrees remain distinct even when repository identity is shared.

**Failure injection**

- Change files while the watcher is stopped; rescan after restart finds the drift
  and identifies the observation gap.

**Exit gate**

Two fixture agents deliberately overlap and Crewfold blocks or warns exactly as the
configured deterministic policy specifies. Semantic/embedding similarity is not
required.

### M13 — Structured meetings and consolidation

**Question answered:** Can two or three agents resolve overlap through a durable,
bounded procedure that changes downstream work?

**Visible result**

```sh
crewfold meeting create --from-overlap OVERLAP_ID \
  --participant agent-a --participant agent-b --facilitator manager
crewfold meeting run MEETING_ID --fixture positions-and-proposal.json \
  --expected-revision 1
crewfold meeting inspect MEETING_ID
crewfold meeting accept MEETING_ID --expected-revision 2
```

**Deliverables**

- Meeting agenda, participant, frozen input, contribution, resolution, and action
  records.
- Independent first-round contributions and facilitator round.
- Resolution policies: owner decision, named reviewer, or constrained manager
  proposal.
- Resolution actions that can sequence, split, reassign, designate explicit
  implementer/reviewer task duties, or cancel tasks. These duties are
  task-specific workflow metadata, not `AgentDefinition.Role` authority.
- Stalled/timeout handling and human takeover.

**Automated acceptance**

- Two-agent overlap results in an ordered task dependency.
- Three-agent scenario designates one implementer and one reviewer.
- Missing participant stalls visibly without discarding received contributions.
- Re-running a meeting step does not ask a participant twice for the same round.
- Unapproved resolution cannot mutate claims or assignments when policy requires
  owner acceptance.

**Failure injection**

- Stop the facilitator after all positions arrive; restart and resume from the
  durable frozen input without recollecting positions.

**Exit gate**

The accepted meeting result changes real task/claim state and is fully explainable
from records without reading terminal transcripts.

### M14 — Canonical knowledge and explicit context packets

**Question answered:** Can Crewfold preserve an accepted decision or finding and
deliver it explicitly to a replacement agent without copying a transcript?

**Implementation status:** Complete. The checked-in replacement-agent scenario,
failure and rollback proof, persistence/restart proof, full repository gate, and
[milestone review](reviews/canonical-knowledge.md) pass the normal exit gate.

**Visible result**

```sh
crewfold knowledge propose finding.md --workspace personal --type finding \
  --from-task TASK_ID --socket "$socket"
crewfold knowledge accept KNOWLEDGE_REVISION --expected-state-revision 1 \
  --workspace personal --socket "$socket"
crewfold context build NEXT_TASK --workspace personal --agent replacement \
  --include KNOWLEDGE_REVISION --expected-task-revision 2 --socket "$socket"
crewfold context show CONTEXT_PACKET_ID --workspace personal \
  --socket "$socket" --output json
crewfold context explain CONTEXT_PACKET_ID --workspace personal \
  --socket "$socket" --output json
```

**Deliverables**

- Stable items and immutable-content revisions for `decision` and `finding`, with
  separate review/currency state, freshness, and explicit supersession.
- Frozen provenance from tasks, concluded meetings, and accepted meeting proposals
  from concluded meetings; the primary source derives project scope and optional
  task scope narrows applicability.
- Owner acceptance/rejection/staleness/supersession with durable authority checks,
  plus authenticated agent-run proposal without agent governance authority.
- The current context packet using ordered exact revision links only, with a 32 KiB total
  limit and 12 KiB whole-item knowledge sub-budget.
- Packet output preserving exact requests, plus explanation of inclusions,
  exclusions, replacement metadata, revisions, and total/knowledge byte
  accounting.
- No transcript ingestion, search, automatic curation, implicit retrieval, or
  context deltas.

**Automated acceptance**

- An accepted decision appears in a new packet at the requested revision.
- Proposed, rejected, stale, superseded, out-of-scope, and over-budget items are
  excluded with stable per-revision reasons; an unknown ID fails the build.
- Superseding an item leaves history intact. An exact old pin is excluded with
  replacement metadata, and only an explicitly requested current successor can be
  included.
- A task source defaults to project-wide applicability; explicit task scope is
  enforced without changing the source-derived project.
- A provider-switch run continues from handoff plus explicit accepted knowledge.
- Unrelated terminal transcript text is absent from the packet and database.

**Failure injection**

- At the internal governance boundary, attempt to accept a decision as an actor
  without the required authority; preserve the proposal and produce an authority
  denial record without creating accepted knowledge. An authenticated run's
  unadvertised acceptance-tool probe is separately recorded as `run.tool_denied`.

**Exit gate**

One replacement-agent scenario succeeds using explicit canonical knowledge.
Search, broader knowledge types, automatic curation, contradiction handling, and
context deltas remain out of scope for M14.

### M15 — Curator, deterministic retrieval, and context deltas

**Question answered:** Can Crewfold find and maintain relevant knowledge at larger
volume without making retrieval the source of truth?

**Implementation status:** Complete. Independently testable slices implement
deterministic scoped search, explanation, retrieval health, explicit search-index
rebuild, participant-bound cross-project collaboration through the existing
mailbox tools, and a provider-free bounded curator with one exact deterministic
meeting-resolution rule, plus owner-confirmed exact-revision contradictions with
agent-run reporting and fail-closed search/context behavior, and deterministic
project knowledge export/import with exact applicability and contradiction
snapshots, plus bounded live context deltas on the current packet with explicit owner refresh
and exact-run acknowledgement. The milestone review and complete repository gate
pass.

**Visible result**

```sh
crewfold knowledge search "contact ordering" --workspace personal \
  --project demo --socket /path/to/crewfold.sock
crewfold knowledge index status --workspace personal \
  --socket /path/to/crewfold.sock
crewfold knowledge index rebuild --workspace personal \
  --socket /path/to/crewfold.sock
crewfold doctor --retrieval --workspace personal \
  --socket /path/to/crewfold.sock
crewfold thread create --workspace personal --subject "Engine integration" \
  --participant plug-agent=TASK_PLUG --participant engine-agent=TASK_ENGINE \
  --socket /path/to/crewfold.sock
crewfold thread invite THREAD_ID --agent review-agent --task TASK_REVIEW \
  --expected-participant-revision 1 --workspace personal \
  --socket /path/to/crewfold.sock
crewfold curator queue
crewfold context refresh RUN_ID --workspace personal --idempotency-key refresh-1
crewfold context delta list RUN_ID --workspace personal --after-sequence 0 --limit 20
crewfold context delta show CONTEXT_DELTA_ID --workspace personal
crewfold context delta explain CONTEXT_DELTA_ID --workspace personal
crewfold contradiction list
crewfold knowledge export /private/demo-knowledge --workspace personal --project demo
crewfold knowledge import /private/demo-knowledge --workspace personal --project demo \
  --expected-content-sha256 SHA256 --create-scope
```

**Deliverables**

- SQLite FTS5 retrieval with hard scope, authority, and freshness filters plus
  versioned deterministic applicability/provenance/quality/text ranking.
- Curator proposal queue with bounded rule-based auto-acceptance.
- Immutable current-packet base with source cursor, bounded same-project reverse
  dependents, exact participant rosters, collaboration budget, and frozen live
  delivery policy.
- Owner-triggered context refresh plus immutable deltas for bounded message
  previews, accepted decisions, delivered-knowledge withdrawals, no-body disputed
  suppression tombstones, contradiction transitions and eligible decision
  re-offers, reverse dependents, and participant rosters. Direct-upstream drift
  rebases.
- Argument-free run MCP fetch and exact-run ID/sequence acknowledgement; no owner
  acknowledgement or run-triggered scan.
- One pending delta, 1,000 relevant-event, 16 KiB per-delta, and 64 KiB chain
  bounds with no-op cursor advancement and durable fail-closed rebase.
- Exact-pair contradiction workflow and relational dispute state independent of
  current/stale currency.
- Deterministic Markdown plus canonical manifest export and owner-only exact
  empty-scope import.
- Search index health and idempotent rebuild from canonical records.
- Owner-created, participant-bound cross-project threads using the existing
  provider-neutral mailbox surface, with exact agent/task/project authority.

**Automated acceptance**

- Relevant scoped items rank above textually similar out-of-scope items.
- A broad project search cannot expose task-scoped knowledge; a task search may
  return only project-wide or exact-task records.
- The exact ranking order and explanations survive daemon restart.
- Retrieval can suggest only candidates; it cannot make proposed content accepted.
- Accepting an applicable decision alone creates no delta; one explicit owner
  refresh creates exactly one delta, pending refresh returns it unchanged, and
  only the exact live run can fetch/acknowledge it.
- Pending delivery, acknowledgement replay, and inspected cursors survive daemon
  restart; a no-change refresh advances without an empty delta or event.
- Current-packet rosters/message previews remain exact-task scoped, contradiction and
  withdrawal transitions are whole and explainable, closure re-offers only still
  eligible decisions, and a legal new reverse dependent appears without granting
  authority.
- A changed base contract requires rebase and cannot invent live tools.
  Relevant-event, delta, and cumulative overflow rebase rather than truncate.
- Contradictory accepted candidates create a conflict, not a blended summary.
- Deleting or corrupting the search index leaves exact canonical reads unchanged;
  rebuilding changes no canonical revisions or events.
- Export/reimport preserves IDs, all lifecycle states, provenance, task
  applicability, and contradiction effect without creating operational sources.
- A library agent can queue a one-recipient explanation to an offline application
  agent across projects; restart preserves it, while another task run for the same
  agent cannot receive or wake for it.
- Direct mail remains project-isolated; participant creation/invitation is
  owner-only and a stale roster revision changes nothing.

**Failure injection**

- Corrupt or remove the search index and prove canonical reads continue while
  search returns `retrieval_degraded`, `doctor --retrieval` reports degraded
  retrieval, and an explicit rebuild repairs it.
- Restart with one unacknowledged context delta and prove byte-identical local/run
  reads plus exactly-once acknowledgement. Inject delta/store failures after
  projection/event writes and prove state, event, receipt, and idempotency rollback
  together.

**Exit gate**

Retrieval, curator, collaboration, contradiction, portable-knowledge, and live
context-delta scenarios pass over representative provider-free fixtures with
explainable selection and restart/failure behavior. Embeddings remain optional
and disabled.

### M16 — Manager proposals and supervisor scheduling

**Question answered:** Can a manager propose work and can Crewfold advance routine
dependencies while keeping deterministic constraints and human authority?

**Implementation status:** Complete. Implementation commit
`3c7639a3ef54f68030e999015b61a45c32825f72` and the
[passed milestone review](reviews/manager-supervisor.md) record the frozen
proposal/approval boundary, deterministic scheduler policy, concurrency limits,
recovery semantics, full race/scenario gate, and independent zero-defect final
audit.

**Visible result**

```sh
crewfold manager propose-tasks --objective OBJECTIVE_ID
crewfold proposal inspect PROPOSAL_ID
crewfold proposal accept PROPOSAL_ID
crewfold supervisor explain ACTION_ID
```

**Deliverables**

- Manager MCP tools for task, assignment, review, and escalation proposals.
- Owner-revisioned manager grants, optional exact grant snapshots in the current
  packet, and exact project/agent-bound launch profiles.
- Deterministic validation of proposed dependencies, claims, budgets, and policy.
- Dependency-aware ready queue and global/provider/project concurrency bounds.
- Supervisor conditions for blocked, stale, failed, over-budget, dependency-ready,
  and repeated-failure states.
- Explainable recommendations, approved automatic actions, and human approval
  queue.
- Atomic proposal acceptance and durable schedule intent/action/job receipts with
  idempotent recovery before any worker launch.

**Automated acceptance**

- A fixture manager proposes a valid task decomposition that creates no state until
  accepted.
- The granted and ungranted fixture agents share an arbitrary role label; only the
  exact grant authorizes proposal tools.
- One owner acceptance creates a valid `A -> B -> independent review` sequence;
  replay produces no second work or decision.
- Cycles, cross-scope references, authority escalation, finite-budget overflow,
  unlimited-under-finite budget, stale/disabled or wrong-agent profile, and claim
  conflicts are rejected with exact reasons and no partial application.
- Completing A schedules B exactly once and explains the dependency, profile,
  exact profile/agent binding, claims, and capacity decision.
- Global, project, provider, agent, checkout, and claim limits remain satisfied
  under concurrent supervisor scans.
- A stale run is reconciled before lease release or reassignment; `lost` remains
  blocked and consumes capacity.
- A recommendation beyond automatic policy creates one inert approval request;
  only one current owner decision can produce its allowed effect.
- Named cases exercise `dependency_ready`, `blocked`, `stale`, `failed`, wall-time
  `over_budget`, and `repeated_failure`; only dependency-ready scheduling
  auto-applies by default and each other condition yields one approval.
- A current packet without a grant and wrong-scope, expired, or revoked grants are
  denied while one exact current grant permits only its proposal kinds.
- Raw-SQL, named transaction-failure, idempotency, and restart tests prove that
  proposal/decision/application and condition/action/run/job/event receipts cannot
  become partially visible.
- Ready-queue tests prove priority/readiness/ID ordering, metadata-stable readiness,
  deterministic deferral backoff, relevant-fact early wake, and bounded pages
  that do not let deferred heads starve later eligible work.
- Intent lifecycle tests prove exact satisfied/failed/cancelled terminalization,
  bounded retry retention, owner cancellation of pending/deferred or exact-latest
  start-failed work, and rejection of manual assignment while any intent is open.
- Worker tests prove a committed scheduling/retry receipt remains the authority
  for that exact launch after profile retirement, agent disablement or revision
  change, or assignment-deadline passage, while every future placement revalidates
  current authority.

**Failure injection**

- Crash after scheduling intent commits but before worker launch; recovery starts
  the same run at most once and preserves the original placement explanation.

**Exit gate**

An arbitrarily named planning agent with one exact grant and arbitrarily named
execution/evidence agents complete a dependent task sequence, with one deliberate
human approval and a fully inspectable decision trail.

### M17 — Local checks and reusable check-watch capability

Contract: [ADR-0016](decisions/0016-owner-granted-local-check-evidence.md).

**Question answered:** Can Crewfold run and route check results as evidence without
confusing test status with task or merge authority?

**Visible result**

```sh
crewfold check run unit --task TASK_ID
crewfold check watch --project demo
crewfold check inspect CHECK_RUN_ID
```

**Deliverables**

- Allowlisted local check definitions and direct-runtime execution.
- Structured check run, result, logs/artifacts, HEAD, and freshness records.
- Owner-granted check-watch capability attachable to any eligible agent, with
  routing rules for agents assigned task ownership, evidence-review, or
  coordination duties.
- Policy-controlled repair-task proposal.
- Invalidated/stale result handling when repository HEAD changes.
- Evidence classification that distinguishes agent self-report, mechanical check,
  independent review, and policy acceptance.
- Current-packet exact project/agent/definition/operation grant snapshots,
  mutually exclusive with manager grants. `AgentDefinition.Role` and
  `LaunchProfile.Purpose` are never authority or routing inputs.
- A separate `requested -> starting -> running -> finished` check lifecycle with
  durable launch receipt, stable direct-runtime operation ID, sealed-spec replay,
  status-only inspection, and bounded redacted retained logs.
- First-class task check requirements so one result links only one named
  criterion. Verification requires matching clean launch/terminal HEAD; dirty
  passes are diagnostic/unknown and later observed staleness is monotonic.
- Exact task-assignment and owner duty routes with honest
  `crewfold-check-worker` subsystem message provenance. Missing owner is visible
  `unroutable` state.
- Repair proposals disabled by default, policy/profile bounded, inert until one
  current local-owner decision.

**Automated acceptance**

- Passing check satisfies only the named acceptance criterion.
- Failing check attaches evidence and notifies the correct task owner.
- HEAD change marks the old result stale.
- Missing or stale checks remain visible and cannot be summarized as verified.
- Timeout, crash, and excessive output retain a bounded diagnostic artifact.
- No result automatically pushes, merges, or determines integration order.
- A check result never changes task lifecycle, creates independent review, or
  records policy acceptance. Direct local checks are trusted owner-authored
  commands rather than a sandbox/no-network promise.

**Failure injection**

- Stop the daemon during a running check; reconcile the child and record one result
  or an explicit unknown outcome without executing it twice.

**Exit gate**

Arbitrarily named change/evidence agents and one agent with a check-watch grant
complete a fixture change/fail/repair/pass cycle. Remote GitHub/GitLab CI remains
deferred.

## Stage E — Make the personal product dependable

### M18 — Outcome ledger and management briefings

**Question answered:** Can one person understand what a project achieved, why,
how much to trust it, what remains, and what needs attention after more agent work
than they can personally inspect?

**Visible result**

```sh
crewfold outcome commitment add release-ready --task TASK_ID \
  --title "Release-ready deliverable" --criterion "owner criterion"
crewfold outcome propose --task TASK_ID outcome.yaml
crewfold outcome show OUTCOME_ASSESSMENT_ID
crewfold outcome list --task TASK_ID
crewfold outcome accept OUTCOME_ASSESSMENT_ID --expected-state-revision 1
crewfold outcome reject OUTCOME_ASSESSMENT_ID --expected-state-revision 1
crewfold checkpoint create --project demo
crewfold checkpoint show CHECKPOINT_ID
crewfold checkpoint list --project demo
crewfold briefing show --project demo --since CHECKPOINT_ID
crewfold briefing explain BRIEFING_CLAIM_ID --briefing BRIEFING_ID
```

**Deliverables**

- Explicit deliverable commitments and revisioned outcome assessments with
  proposed, accepted, rejected, and superseded review states plus achieved,
  partial, not-achieved, and unknown conclusions.
- Outcome links to decisions, evidence, verification strength and freshness,
  compatibility/stability effects, risks, unknowns, and follow-up tasks.
- Deterministic projections across task, objective, project, and workspace scope.
- Owner checkpoints and bounded change briefings with stable structured output.
- Claim-level explanation and drill-down to the durable records and event cursor.
- One current structured representation with no model or narrative renderer.
- Strict proposal JSON/YAML with the exact `{commitment, assessment}` wrapper;
  task scope remains explicit CLI/API authority input and is cross-checked against
  the pre-existing commitment.
- Current captured workspace high-water plus an optional exact checkpoint as an
  exclusive lower bound; callers cannot select a historical event cursor.

**Automated acceptance**

- A completed run and proposed handoff do not appear as accepted delivery until
  the local owner accepts an explicit outcome assessment.
- A transcript-free fixture with at least ten agent/task histories answers what
  changed, why, how much to trust it, what remains, and what needs the owner.
- Self-reported, mechanically checked, independently reviewed, stale, missing,
  disputed, and contradictory support remain distinguishable.
- Accepted partial conclusions and superseded assessments preserve unmet
  commitments and history.
- Duplicate work, compatibility effects, unresolved risks, and unknowns appear in
  the correct project briefing without flooding unrelated scopes.
- “Since checkpoint” includes each relevant change once, stays within its size
  budget, and every material claim has a stable provenance path.
- Caller evidence is closed to handoff or exact check-requirement evidence;
  classification, freshness, strength, and current truth are derived.

**Failure injection**

- Stop the daemon after committing an outcome assessment before returning its
  response; restart returns the one committed assessment and a stable briefing
  with no duplicate checkpoint change.

**Exit gate**

The owner can make a defensible continue, review, redirect, consolidate, pause, or
stop decision from the project briefing without opening provider transcripts.
This proves evidence-backed management compression, not merely status display.

### M19 — Operator TUI

**Question answered:** Can one person navigate management briefings, active
exceptions, and live crew state and intervene without polling terminal panes or
raw event records?

**Visible result**

```sh
crewfold ui
```

**Deliverables**

- Terminal dashboard for project briefings, accepted outcomes, risks, unknowns,
  required decisions, ready/blocked tasks, active runs, inbox, overlaps, meetings,
  checks, approvals, and recent explanations.
- Drill-down from summary to entity timeline and then Herdr attach.
- Keyboard-only operation and safe resize behavior.
- Read-only default; mutations reuse normal command/policy APIs.

**Automated acceptance**

- State-model and rendering tests for empty, normal, degraded, and large views.
- Non-interactive smoke test consumes a deterministic event fixture.
- Reconnect resumes from an event cursor without duplicating notifications.
- Every displayed urgent count links to the records that produced it.
- Briefing sections retain the same claims, evidence classifications, and event
  cursor as the CLI/API projection.

**Failure injection**

- Restart the daemon while the TUI is open; show disconnected/reconnecting state,
  resume, and preserve the user's selected entity when possible.

**Exit gate**

The full personal scenario is operable through the TUI while every mutation remains
equally available through the CLI/API.

### M20 — Personal-scale hardening and recovery

**Question answered:** Can Crewfold remain controllable, recoverable, restorable,
and canonically intact at the target local scale without cloning live runtime
authority?

**Visible result**

```sh
crewfold doctor --full --socket /path/to/crewfold.sock
crewfold backup create --socket /path/to/crewfold.sock \
  --to /new/private/backup-directory
crewfold backup verify /new/private/backup-directory
crewfold backup restore /new/private/backup-directory \
  --to /new/private/crewfold-data
crewfold backup activate /new/private/crewfold-data \
  --confirm-source-retired
crewfold repair inspect /path/to/crewfold-data
crewfold test load --profile personal-100
```

**Deliverables**

- Replace the migration ladder with one exact `baseline/current.sql`, compiled
  baseline hash, installed `sqlite_schema` hash, and full table/queue auditor.
  Empty storage is created; any other baseline is refused without DDL or an
  import/upgrade path.
- An online `backup.create` daemon method and path-based CLI. It snapshots with
  SQLite's online backup API, validates the captured cut as fully quiescent, and
  publishes an absent-or-complete owner-private bundle.
- Offline, source-independent verify and restore-to-new-directory commands. A
  restored directory remains inert until the owner explicitly confirms source
  retirement and activation generates a new node key and empty capability/runtime
  roots.
- Bundles contain only the standalone database and snapshot-referenced immutable
  check and bounded redacted run-log artifacts. They exclude node keys,
  capabilities, live runtime/check-runtime state, provider homes, repositories,
  WAL/SHM files, and orphan files.
- Opaque runtime/provider handles become node-bound internal live bindings, leave
  public records and event payloads, and are cleared on terminal transitions.
  `run resolve-lost --confirm-runtime-retired` is the only owner path that frees
  capacity held by an uncertain external runtime.
- Full online doctor and offline read-only repair inspection with exact current-
  baseline, canonical, event, receipt, queue, artifact, derived-index, filesystem,
  restore, and resource diagnoses.
- Workspace admission limits apply equally to manual and supervised starts:
  defaults eight unresolved, two starting, four per project/provider, plus a
  node-wide hard ceiling of 20. Message wake and every other external effect move
  off request transactions into bounded workers.
- A deterministic provider-free profile with one workspace, ten projects, 100
  arbitrary-role agent definitions, 1,000 tasks, exactly 100,000 current events,
  80,000 in one noisy project, and a bounded active phase.
- Management-workload, queue saturation, database-busy, crash/restart, runtime-
  loss, duplicate-delivery, unavailable-provider, security/redaction, short
  endurance, and local reproducible Linux-package candidate tests.

**Automated acceptance**

- `CUR-*` proves exact fresh/restored baseline and full canonical auditor
  coverage; there is no same-version divergent-schema adoption.
- `BKP-*` proves a WAL-safe snapshot cut, quiescence refusal, DB/artifact binding,
  source-independent verification, path/mode/hash safety, idempotent replay, and
  absent-or-complete crash behavior.
- `RST-*` proves source-independent new-directory restoration, pending-state
  startup refusal, explicit source-retired activation, a fresh node key/no
  capabilities, exact domain cursor, and pre-worker rejection of injected live
  state.
- `RUN-*` proves public/event handle removal, immutable terminal logs, and
  explicit owner resolution of lost capacity.
- `HLT-*`, `LOAD-*`, `BP-*`, `DB-*`, `FLT-*`, `END-*`, `SEC-*`, and `PKG-*`
  prove diagnostics, exact load counts, noisy-project fairness, bounded
  responsiveness/resources, queue isolation, busy/restart behavior, no leaks,
  recovery redaction, and a reproducible unpublished Linux candidate. Exact rows
  and thresholds are frozen in [testing.md](testing.md#m20-executable-acceptance-and-security-matrix).

The absolute Linux gate is warm startup within two seconds; saturated status and
message p99 at most one second and maximum two seconds over 200 operations;
project briefing p99 at most two seconds/maximum five; workspace briefing p99 at
most five seconds/maximum ten over 20 reads; doctor/create/verify/restore at most
60 seconds each; load generation and verification at most five minutes; peak RSS
at most 512 MiB; and database plus referenced fixture artifacts at most 1 GiB.
Relative benchmark changes are reported but are not the sole gate.

**Failure injection**

- Saturate one provider and all starting slots while status, message send, lease
  reconciliation, and another provider continue. Kill create/restore at every
  publication barrier and kill runtime/wake workers before and after their
  external-effect boundary. Hold an SQLite writer, remove/truncate artifacts,
  inject a lost runtime, and attempt first startup of tampered restored state.

**Exit gate**

The complete M20 matrix and every prior scenario pass repeatedly with no manual
database edit, old-schema/bundle compatibility path, cloned runtime authority,
leaked process/socket/file descriptor/temp directory, hidden cleanup, paid
provider call, or role/purpose authority. ADR-0019's non-goals remain explicit:
no active-run hot backup, in-place/PITR/incremental recovery, automatic canonical
repair, old-baseline conversion, cloud/encrypted/signed backup, general GC,
multi-node failover, replacement UI, public package publication, or license
decision.

### M21 — Local web workbench

**Implementation checkpoint:** Complete. Exact XDG service/open lifecycle, pinned
embedded React assets, loopback bootstrap/session/CSRF security, strict canonical
RPC plus SSE invalidation, browser onboarding, durable owner conversation through
a visible provider-backed project executive, explicit typed-proposal acceptance, work/crew/inbox/
decision/evidence/activity/briefing/health surfaces, bounded Git and logs, and a
single-use run-bound terminal WebSocket are implemented. A built-binary real-Chrome
scenario begins with an empty store and uses browser controls to onboard a committed
repository, direct the executive, review and accept typed work, inspect its agent, and
generate the exact project briefing. Commit `d1b5dd8` completes the missing
event-driven orchestration seam: worker reports and agent messages now trigger one
coalesced, restart-safe exchange with that same executive, which can publish a cited
update, raise one owner decision, or submit successor work for explicit review.

**Question answered:** Can one developer start, direct, inspect, understand, and
intervene in a real local crew from one owner interface instead of composing the
control plane through commands?

**Visible result**

```sh
crewfold service install
crewfold open
```

The browser opens the local workbench. From a fresh exact-current installation,
the owner selects an existing Git repository, verifies a provider, describes an
objective, lets Crewfold plan and execute work inside explicit policy, inspects
one agent live, decides an exception, and reviews the resulting evidence without
using another CLI command.

**Contract**

The accepted architecture and product boundary is
[ADR-0020](decisions/0020-local-web-workbench.md). The interactive design
reference is [`../web/workbench-mock.html`](../web/workbench-mock.html). The mock
does not define a second source of truth; every displayed fact and effect comes
from the daemon's canonical APIs.

**Delivery slices**

1. **Service and private defaults.** Add a Linux user service, XDG-derived
   state/config/runtime paths, `crewfold service install|start|stop|status`, and
   `crewfold open`. Retain explicit foreground paths for development and isolated
   tests. Installation creates no workspace, project, provider call, or model
   charge.
2. **Secure embedded web shell.** Pin React, TypeScript, Vite, Node, and pnpm for
   builds; embed content-hashed static assets in the Go
   binary. Serve exact-loopback HTTP JSON and SSE behind a single-use local
   bootstrap and bounded owner session. Render health, connection state, current
   workspace/project selection, and canonical read-only records first.
3. **Browser onboarding.** Create/open the first workspace, choose an existing Git
   repository, register the project/checkout, diagnose Codex or Claude, preflight
   live Herdr as the normal interactive runtime, and freeze an initial
   concurrency/cost/scope policy. Direct remains an explicit advanced CI/headless
   fallback. The browser never receives provider credentials.
4. **Durable command workbench.** Add owner conversations, turns, frozen typed
   operation graphs, policy evaluations, approval links, execution states, and
   exact effect receipts. Queries append no domain effect. An explicit act turn
   executes currently allowed operations without a redundant confirmation and
   shows the committed receipts. Unknown, stale, malformed, over-budget,
   out-of-scope, externally effective, destructive, or materially ambiguous work
   pauses before its first gated effect.
5. **Planning and launch.** Reuse manager grants/proposals, launch profiles,
   supervisor policy, task assignment, context, run admission, and approvals to
   produce one editable objective/task/dependency/agent/budget plan. Accepting or
   directly authorizing the plan launches every dependency-ready operation allowed
   by policy and exposes the remaining gates.
6. **Agent inspector.** Present canonical task/run/context/message/claim/check/
   evidence/history state, bounded Git status/diff observations, budget/capacity,
   direct logs, and controls. Add a short-lived run-bound WebSocket terminal grant
   for a current live interactive runtime. Herdr is the normal runtime and is
   managed as a companion user service when installed; Direct remains available
   for an explicitly headless workflow.
7. **Understanding surfaces.** Bring the exact M18/M19 Briefing, Work, Decisions,
   Checks, Coordination, and Activity facts into the browser without client-side
   reclassification. Add portfolio/project navigation, inbox, evidence drill-down,
   visible stale/reconnect state, and progressive responsive layouts.
8. **Hardening and browser-only acceptance.** Prove restart, idempotent replay,
   browser security, hostile text, bounded transport/rendering, accessibility,
   personal-100 scale, provider-free onboarding/execution through the explicit
   Direct fallback, recorded subscription authentication, Herdr-first interactive
   operation, and every prior gate.
9. **Event-driven manager loop.** Advance one durable project review cursor in
   the same transaction as each worker report or message, coalesce concurrent
   activity, run a read-only manager review through the selected provider/runtime,
   and persist its update, typed owner decision, or inert reviewed graph. Restart
   replays the exact event cut and completed structured output without another
   model interpretation.

**Deliverables**

- Owner-local service lifecycle with safe default paths and exact diagnostics.
- Embedded React/TypeScript workbench with no production Node process or Electron.
- Loopback-only authenticated HTTP/SSE and run-scoped terminal WebSocket boundary.
- First-run repository/provider/runtime/policy onboarding.
- Durable conversation-to-typed-command execution and receipt model.
- Editable plan, automatic within-policy execution, and exact approval cards.
- Durable worker-to-manager review with visible updates, owner questions, and
  reviewed successor graphs; no timer-only browser simulation.
- Agent inspector with canonical activity/evidence and optional live terminal.
- Browser presentations of the current work graph, inbox, decisions, checks,
  coordination, activity, briefings, health, and recovery guidance.
- Updated interface documentation that presents web as primary, CLI as
  automation/recovery/administration, TUI as an operational fallback, Herdr as an
  optional runtime, and MCP as the agent surface.

**Automated acceptance**

- One provider-free browser scenario starts from no data, registers a disposable
  game repository, configures a fixture provider, enters “build the first playable
  vertical slice; organize and start,” and observes exact objective/task/
  assignment/run receipts.
- The scenario opens the assigned agent, proves context/activity/changed-scope/
  check/history parity, accepts one gated decision in conversation, sees successors
  start, restarts browser and daemon, then redirects/stops work and reads an exact
  outcome briefing.
- Recorded Codex fixtures prove a ChatGPT-subscription-authenticated CLI path with
  no API key in browser, daemon state, arguments, environment, logs, or events.
- Store and full-daemon tests prove report/message coalescing, manager-originated
  turn provenance, exact event cursors, restart lease recovery, completed-output
  reuse, and browser-visible review state without a second owner request.
- Browser transport suites prove origin/host/CSRF/bootstrap/session/terminal-grant
  isolation, strict schemas, idempotent replay, bounded streams, and fail-closed
  unknown fields/events.
- Component and browser suites prove keyboard access, focus, screen-reader names,
  contrast, reduced motion, narrow/desktop layouts, hostile-text sanitization, and
  no unbounded DOM list.
- The personal-100 fixture remains inside current M20 daemon limits while browser
  bootstrap, project switching, activity, work graph, crew list, and agent detail
  meet the M21 budgets frozen in `docs/testing.md`.

**Failure injection**

- Restart the service during bootstrap, manager interpretation, multi-operation
  execution, SSE refresh, and terminal streaming. Lose the first committed
  response, reuse an operation key with changed semantics, exceed budget/capacity,
  inject a malformed plan, remove Herdr, move a pane, and close it unexpectedly.
- Attempt cross-origin and DNS-rebinding requests, missing/expired/replayed
  bootstrap and terminal grants, CSRF, wildcard Host, frame embedding, remote bind,
  capability/provider-secret disclosure, path traversal, oversized payloads,
  terminal control injection, and another run's terminal.

**Exit gate**

After one installation/open action, the complete provider-free personal workflow
is operable and understandable through the browser without CLI composition. An
explicit owner instruction executes permitted work with exact durable receipts;
gated work stops before its first unauthorized effect. Browser/daemon restart
preserves the conversation and canonical work, agent inspection remains honest,
Herdr is the normal interactive runtime, Direct remains the advanced headless
fallback, Codex subscription authentication needs no API key, M20 scale
and recovery claims remain true, and every previous scenario passes. No hosted,
remote, multi-user, push/deploy/publication, public-package, or compatibility path
is introduced.

### M22 — Public open-source release readiness

**Question answered:** Can an unrelated developer install, understand, test, and
extend Crewfold safely?

**Visible result**

```sh
crewfold demo personal
crewfold adapter conformance ./path/to/adapter
```

**Deliverables**

- Approved open-source license and contribution/governance policy.
- Installation packages, tutorial, example project, and release notes.
- Adapter SDK, one current adapter contract, and conformance command.
- Public security contact and threat model.
- Reproducible release build and current-baseline backup/restore documentation.
- Name/namespace review before any upstream repository is created.

**Automated acceptance**

- Fresh-machine installation test in supported environments.
- Tutorial is executable and verified in CI with fake providers.
- Release artifact provenance/checksum verification.
- Third-party sample adapter passes conformance without importing internal packages.

**Failure injection**

- Install an incompatible adapter and verify a safe refusal with exact protocol
  mismatch details.

**Exit gate**

The release candidate can be evaluated fully without model credentials, while live
Codex/Claude/Herdr tests remain opt-in. Creating a public upstream still requires
an explicit owner decision.

## Release landmarks

| Landmark | Milestones | What is genuinely usable |
| --- | --- | --- |
| Kernel preview | M0–M2 | Local daemon, database, and events |
| Single-agent preview | M3–M7 | One deterministic task/run/MCP loop |
| Multi-agent preview | M8 | Durable communication between agents |
| Interactive preview | M9–M11 | Herdr plus real Codex and Claude canaries |
| Coordination preview | M12–M13 | Overlap detection and structured resolution |
| Knowledge preview | M14–M15 | Canonical knowledge, context, curation, and retrieval |
| Personal alpha | M16–M17 | Manager/supervisor flow and local check evidence |
| Management alpha | M18 | Evidence-backed outcomes and bounded owner briefings |
| Operator alpha | M19 | One coherent terminal control surface |
| Personal beta | M20 | Operable and recoverable at target local scale |
| Usable personal workbench | M21 | One local browser surface for intent, execution, inspection, and understanding |
| Public release candidate | M22 | Installable, documented, extensible OSS package |

These are capability labels, not promises of semantic-version numbers or dates.

## What we deliberately do not build ahead of proof

- No TUI before the CLI and event stream expose real state.
- No real model integration before a fake adapter passes the whole loop.
- No Claude adapter bundled into the Codex milestone.
- No meetings before durable messages and deterministic overlap exist.
- No curator before task handoffs and accepted decisions have somewhere to live.
- No embeddings before deterministic retrieval has a measured recall problem.
- No distributed queue, PostgreSQL, or hosted service in the personal milestones.
- No automatic push, merge, deployment, or communication with real people.

## Milestone review packet

Before marking any milestone complete, produce a review record using the
[milestone review template](reference/milestone-review-template.md), containing:

- exact commit under review;
- acceptance scenario command and captured structured result;
- automated test list and results;
- failure injected and observed diagnosis;
- schema/current-baseline changes;
- security or autonomy-policy changes;
- known limitations and explicitly deferred work;
- next milestone entry criteria.

This record makes regressions traceable and prevents “mostly complete” milestones
from becoming invisible prerequisites for later work.
