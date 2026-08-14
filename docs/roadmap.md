# Roadmap

## How to read this roadmap

This is the compact delivery sequence. The exact deliverables, acceptance
commands, failure cases, and exit gates are in the
[implementation plan](implementation-plan.md). The test infrastructure and quality
rules are in the [testing strategy](testing.md).

Current status: **M0 through M20 are complete. M21 implementation is underway.
Its first vertical slice provides private XDG paths, the Linux user-service
lifecycle, `crewfold open`, embedded React assets, one-time owner bootstrap, and
authenticated live daemon status; onboarding and work orchestration remain.** Evidence is
recorded in the [M0 review](reviews/buildable-repository.md), [M1
review](reviews/daemon-api-spine.md), and [M2
review](reviews/persistent-workspace.md), and [M3
review](reviews/projects-checkouts.md). [M4 evidence](reviews/durable-coordination.md)
covers durable coordination. [M5 evidence](reviews/deterministic-execution.md)
captures deterministic run execution, and [M6 evidence](reviews/direct-runtime.md)
captures supervised direct execution. [M7 evidence](reviews/scoped-mcp.md)
captures run-scoped MCP and immutable briefing. [M8 evidence](reviews/agent-messaging.md)
captures offline-safe durable agent mail and request/reply coordination. [M9
evidence](reviews/herdr-runtime.md) captures the provider-free Herdr fixture
runtime, stable terminal identity, interactive controls, and schema gate. The M9
implementation commit is `c2ce4b6d9783aaa4a09269469e6f3607916a993d`. The M8
implementation commit is `bc6235d76ee45d98a94c8e01b024c69b9eb2299f`. The M7 implementation commit is
`99e9791d39e2c0b3e36333f366a4fd84bcbaf6ef`. The M6 implementation commits are
`2c4043dd86bc1c22938184b3a65835b9754f7db0` and
`951485b894273130941ae1b3a39a76a7267e2c15`. The M5 implementation commit is
`ba57abefc94743ea9dbf0a4c1e1bb6addffaf242`. The durable-coordination
implementation commits are
`7973bded9f99e965bc01a662b6b4d532e679d2c3` and
`dbce60007de652d09862a8f673886702ba9860bc`, with assignment-policy coverage in
`c821ab12cc649d7807a504ee615e61796591178e`. [M10
evidence](reviews/codex-canary.md) covers the Codex adapter, doctor, run-scoped
STDIO MCP bridge, recorded endpoint, and owner-authorized disposable live canary.
Its implementation commits are `d8f6aac5060eb31e380c95c9d01c8aa2dddadd49`
and `676c0bc15a31ef9b2b8233961d2b6eed696bd1c1`.
M11's [passed implementation audit](reviews/claude-canary.md) covers the Claude
adapter and deterministic Codex-to-Claude provider switch. The implementation
commit is `31c8fad1790b738e86516b119c6594293b9c99ba`; the installed-Claude canary is
retained as optional release conformance.
M12's [claims and overlap review](reviews/claims-overlap.md) covers leased declared
scope, exact path-glob intersection witnesses, deterministic policy response,
restart-aware Git drift, and separate checkout attribution. Its implementation
commit is `f756d7c427a82f3661997ccacdbe94ab1d085b36`.
M13's [structured meeting review](reviews/structured-meetings.md) covers frozen
inputs, restart-safe independent positions, owner/reviewer/bounded-manager
authority, typed consolidation actions, human takeover, and the pinned `sqlc`
persistence boundary. Its implementation commit is
`432e6e02a85c4793ec26f08bfcfc7783a587d04d`.

M14's [canonical knowledge review](reviews/canonical-knowledge.md) covers immutable
decision/finding revisions, structured provenance, owner governance, authenticated
agent proposals, exact-link current context packets, restart and rollback evidence, and
recorded Codex-to-Claude replacement without transcript ingestion. Its
implementation commit is `e37fdcf32b2e5f69766405d6585ff24277a1ab3c`.

M15's [curation, retrieval, collaboration, and live-context
review](reviews/curation-retrieval-live-context.md) covers deterministic scoped
retrieval, participant-bound cross-project mail, bounded curation, exact
contradictions, portable project knowledge, and current-packet live context deltas.
Its final implementation commit is `c975ba4b17856b25c16dc5976d248e09a865d178`.

M16's [passed manager/supervisor review](reviews/manager-supervisor.md) records
executable public restart/background scheduling, exact arbitrary-role authority
separation, proposal and approval matrices, readiness/backoff and capacity
boundaries, intent lifecycle, frozen worker authority, fault/raw-SQL defenses,
protocol coverage, the complete stable-tree race/scenario gate, and an independent
final audit with zero unresolved defects. Its implementation commit is
`3c7639a3ef54f68030e999015b61a45c32825f72`.

M17's [passed local-check review](reviews/local-checks.md) records exact
grant-only check authority, the separate recoverable check lifecycle, mechanical
evidence and monotonic freshness, honest subsystem routing, inert repair
proposals, the single current schema, and the complete race/scenario gate. Its
implementation commit is `91d4cb4d3f62f058d20c9b18bc2d408b988e78b8`.

M18's [passed outcome-briefing review](reviews/outcome-briefings.md) records
owner-only deliverable governance, derived evidence trust and freshness,
restart-safe bounded projection, exact claim provenance, current-only briefing
truth, and the complete integrity, race, and public-scenario gate. Its
implementation commit is `2e7ee9882173a91a7997dead40aaccd091fcb901`.

M19's [passed operator-TUI review](reviews/operator-tui.md) records the Go-native
dashboard, exact canonical-read and applied-cursor boundary, owner-reviewed
interventions, terminal safety, restart/reconnect behavior, bounded resources,
and the complete provider-free public scenario. Its implementation commit is
`12cc6e147a6d5ffc92c15708656fecdcaec3d98c`.

M20's [passed personal-beta review](reviews/personal-beta.md) records the exact
current baseline, full integrity/health boundary, quiescent source-independent
backup and restore activation, node-bound runtime safety, deterministic
personal-100 scale envelope, endurance/fault/security matrix, reproducible Linux
candidate, and complete prior-scenario gate. Its implementation commit is
`359522d8aa58a18a7d1151584a8f9bc48b4bfc56`.

## Sequence

| Milestone | Increment | Demonstrable outcome | Depends on |
| --- | --- | --- | --- |
| M0 ✓ | Buildable repository | Build one binary; run version/help/self-check and local CI | Documentation baseline |
| M1 ✓ | Daemon/API spine | Start, query, diagnose, and cleanly stop a local daemon | M0 |
| M2 ✓ | Persistent workspace | Commit an event, restart, restore, and inspect the same workspace | M1 |
| M3 ✓ | Projects/checkouts | Register and observe a disposable Git repository without mutating it | M2 |
| M4 ✓ | Agents/tasks | Define durable agent descriptions, tasks, dependencies, leases, and readiness | M3 |
| M5 ✓ | Fake-agent loop | Assign, launch, progress, block, complete, and hand off one task | M4 |
| M6 ✓ | Direct runtime | Run and recover a real fixture subprocess with bounded output | M5 |
| M7 ✓ | MCP/briefing | Let a run read scoped context and report through authenticated MCP | M6 |
| M8 ✓ | Agent messaging | Exchange and acknowledge durable mail while one agent is offline | M7 |
| M9 ✓ | Herdr runtime | Run the fixture agent in Herdr and reconcile terminal lifecycle | M8 |
| M10 ✓ | Codex canary | Complete the scoped MCP loop with a real disposable Codex session | M9 |
| M11 ✓ | Claude portability | Complete the same recorded loop with Claude and switch providers via handoff | M10 |
| M12 ✓ | Claims/overlap | Detect declared and observed conflicting work deterministically | M8, M9 |
| M13 ✓ | Meetings | Resolve a two-/three-agent overlap into durable task/claim changes | M12 |
| M14 ✓ | Canonical knowledge | Deliver explicitly accepted decisions/findings without transcripts | M13 |
| M15 ✓ | Curator/retrieval | Retrieve, curate, exchange, export, dispute, and incrementally deliver canonical context under bounded authority | M14 |
| M16 ✓ | Manager/supervisor | Propose work and advance dependencies under explainable policy | M15 |
| M17 ✓ | Local checks/check-watch capability | Route fresh check evidence without granting merge authority | M16 |
| M18 ✓ | Outcome briefings | Explain accepted delivery, rationale, evidence, risk, and owner decisions | M17 |
| M19 ✓ | Operator TUI | Understand and intervene in the crew from one terminal dashboard | M18 |
| M20 ✓ | Personal beta | Back up, restore, verify current-baseline integrity, and load-test 100 registered agent definitions | M19 |
| M21 | Local web workbench | Orchestrate, inspect, understand, and intervene from one owner-local browser interface | M20 |
| M22 | OSS release candidate | Install, demo, and extend Crewfold from a clean environment | M21 |

## Capability ladder

```text
buildable binary
  -> observable daemon
  -> durable state
  -> repository awareness
  -> tasks and owner-defined agent descriptions
  -> one fake agent loop
  -> one real process
  -> provider-neutral MCP
  -> two-agent communication
  -> Herdr execution
  -> Codex
  -> Claude
  -> overlap detection
  -> meetings and consolidation
  -> canonical knowledge
  -> curation and retrieval
  -> manager and supervisor
  -> local checks and reusable check observation
  -> outcome ledger and management briefings
  -> operator TUI
  -> personal-scale recovery
  -> local web workbench
  -> public release readiness
```

Each arrow is a release boundary. Later milestones may not redefine earlier domain
semantics merely to make an integration easier; they must pass the earlier
acceptance scenarios under the one current contract or deliberately update those
scenarios in the same greenfield change.

## Release landmarks

### Kernel preview — M0 through M2

Crewfold is a trustworthy local process with inspectable durable state. It does not
yet manage source code or agents.

### Single-agent preview — M3 through M7

One deterministic agent can perform a complete task through the same public
interfaces intended for real providers.

### Multi-agent preview — M8

Two stopped/running fixture agents can exchange durable, scoped communication.

### Interactive preview — M9 through M11

Herdr hosts fixture and real provider sessions. Codex and Claude independently pass
the provider-neutral loop.

### Coordination preview — M12 through M13

Crewfold detects overlap and runs a bounded consolidation procedure whose result
changes task ownership or ordering.

### Knowledge preview — M14 through M15

M14 lets agents resume from owner-accepted decisions/findings linked by exact
revision. M15 adds a curator, deterministic search, participant-bound
cross-project negotiation, contradiction handling, portable project knowledge,
and explicit refresh without making retrieval or conversation authoritative.

### Personal alpha — M16 through M17

Managers propose work, dependencies advance under policy, and local checks route
fresh evidence to explicitly eligible agents. Eligibility comes from exact owner
grants and duty routes, never `AgentDefinition.Role` or
`LaunchProfile.Purpose`.

### Management alpha — M18

Structured outcome assessments and evidence-backed project briefings let the
owner understand delivery, rationale, reliability, risk, unknowns, and required
decisions without reconstructing individual sessions.

### Operator alpha — M19

One coherent terminal dashboard makes the active crew understandable and
intervenable without polling panes.

### Personal beta — M20

One developer can operate and recover the system at the target of roughly 100
registered agent definitions, 1,000 tasks, and 100,000 events with bounded active
concurrency. Recovery is a path-addressed quiescent database-and-artifact cut,
not a clone of node keys, capabilities, or external runtime sessions. A restored
directory remains inert until the owner confirms the source is retired and
activates it with a fresh node key. Role and purpose strings remain arbitrary
descriptive labels rather than an authority taxonomy. The exact contract is
[ADR-0019](decisions/0019-personal-scale-hardening-and-recovery.md).

### Usable personal workbench — M21

One developer opens one owner-local browser workbench, registers an existing
repository, verifies a provider, describes an objective, and lets Crewfold plan
and execute work within explicit policy. The same surface inspects any agent's
canonical task, context, activity, and evidence plus bounded logs or an optional
Herdr terminal. CLI and TUI remain secondary automation, recovery, and operational
interfaces. The exact contract is
[ADR-0020](decisions/0020-local-web-workbench.md).

### Public release candidate — M22

An unrelated developer can install, demo, test, and extend Crewfold without model
credentials. Live provider tests remain explicit opt-in canaries.

## Rules for starting the next milestone

The next milestone starts only when the current milestone has:

- a checked-in, repeatable acceptance scenario;
- automated assertions over public behavior;
- one representative failure-injection scenario;
- restart/recovery proof for its durable state;
- operator-visible diagnostics;
- an explicit list of deferred behavior;
- a milestone review record tied to the commit that passed.

An incomplete milestone can be split into smaller increments. It cannot be declared
done because later code happens to depend on it.

## Parallel work policy

The critical path remains sequential through M9. Small supporting work may happen
in parallel only when its contract is already fixed and independently testable—for
example protocol fixtures, documentation, or a fake adapter.

After M9, provider adapters can be developed independently, but M10 and M11 remain
separate acceptance gates. M12 can also proceed from the fixture-agent path once M9
passes; it does not require either real provider. After M14, retrieval experiments
may proceed only against canonical knowledge fixtures and cannot become authority.

No parallel branch should introduce a second implementation of tasks, messages,
policy, or runtime state.

## Scope controls

The following stay outside the milestone path until the personal beta has proven
the need:

- organization accounts and multi-user tenancy;
- hosted synchronization or remote control plane;
- distributed scheduling or a message broker;
- PostgreSQL, Kubernetes, or required containers;
- dedicated vector database or mandatory embeddings;
- automatic pushes, merges, deployments, or messages to real people;
- hosted or remotely reachable browser control plane;
- autonomous company hierarchy.

The local node and protocols retain extension seams for those later capabilities,
but they do not carry their implementation cost now.

## Immediate next milestone

M21 is next. Its accepted design contract is
[ADR-0020](decisions/0020-local-web-workbench.md), and its interactive reference is
[`web/workbench-mock.html`](../web/workbench-mock.html). The Linux owner-local
service, default private paths, and secure embedded read-only web shell/status
boundary form the first implemented checkpoint. Later slices add onboarding, durable
conversation-to-command execution, editable plan launch, agent inspection,
optional Herdr terminal streaming, and the complete browser-only acceptance path.

The prior OSS release-readiness scope is M22. Packaging, adapter SDK/conformance,
public governance/license work, and any upstream publication stay outside M21 and
must build on its accepted single-place owner workflow.

M20 is complete at implementation commit
`359522d8aa58a18a7d1151584a8f9bc48b4bfc56`; its
[passed review](reviews/personal-beta.md) records the exact current baseline,
full online health and offline repair diagnosis, quiescent private backup,
source-independent verification and restore activation, node-bound runtime
identity, immutable bounded logs, admission/backpressure limits, deterministic
`personal-100` load/fault/endurance gates, reproducible Linux candidate, and the
complete prior-scenario gate.

M19 is complete at implementation commit
`12cc6e147a6d5ffc92c15708656fecdcaec3d98c`; its
[passed review](reviews/operator-tui.md) records exact canonical read and cursor
semantics, owner-reviewed interventions, reconnect and terminal safety, resource
bounds, race coverage, and the real provider-free terminal scenario.

M18 is complete at implementation commit
`2e7ee9882173a91a7997dead40aaccd091fcb901`; its
[passed review](reviews/outcome-briefings.md) records owner-only commitments and
governance, derived evidence trust, bounded restart-safe projection, exact claim
provenance, current-only briefing truth, raw-construction security, race, and
public scenario proofs.

M17 is complete at implementation commit
`91d4cb4d3f62f058d20c9b18bc2d408b988e78b8`; its
[passed review](reviews/local-checks.md) records the exact check grant, separate
runtime saga, evidence/freshness truth, routing, repair, current-schema security,
race, and public scenario proofs.

M16 is
complete at implementation commit
`3c7639a3ef54f68030e999015b61a45c32825f72`; its
[passed review](reviews/manager-supervisor.md) records the exact owner grant,
inert proposal, deterministic supervision, recovery, race, and public scenario
proofs.

M15 is complete: its first independently testable slice
adds scoped FTS5 search on
top of canonical records without allowing retrieval to grant authority: hard
workspace/project/optional-task/authority/freshness filters, versioned
deterministic ranking and explanations, retrieval diagnostics, and an explicitly
rebuildable index whose removal cannot affect exact canonical reads. The accepted
contract is [ADR-0009](decisions/0009-deterministic-derived-knowledge-retrieval.md).

A second completed slice lets agents working in different registered projects
communicate through an explicit owner-created thread. Each participant is bound
to an exact agent, task, and project; direct mail remains project-isolated,
wrong-task runs remain invisible, and each durable message has one recipient. The
accepted contract is
[ADR-0010](decisions/0010-participant-bound-cross-project-collaboration.md).

A third completed slice adds a provider-free curator queue projection and one
owner-configured deterministic rule that can copy an accepted meeting resolution
into canonical knowledge. Derivation is safe while the rule is disabled;
auto-acceptance is an explicit, bounded processing mode with exact provenance and
authority evidence. The accepted contract is
[ADR-0011](decisions/0011-bounded-deterministic-context-curator.md).

A fourth completed slice adds exact-pair contradiction reports, owner confirmation,
bounded dispute inspection, conservative search/context quarantine, and automatic
closure when a participant becomes stale or superseded. Agent reporting is
run/task scoped and never grants governance. The accepted contract is
[ADR-0012](decisions/0012-owner-confirmed-exact-knowledge-contradictions.md).

A fifth completed slice adds byte-stable project `manifest.json` plus derived
Markdown export and exact owner-only import into an empty canonical target. It
preserves task-only applicability without ghost tasks and remains independent of
provider state and FTS. The accepted contract is
[ADR-0013](decisions/0013-portable-project-knowledge-snapshots.md).

The final M15 slice adds explicit context deltas to the current packet under
[ADR-0014](decisions/0014-explicit-bounded-live-context-deltas.md). The immutable
base freezes a journal cursor, bounded reverse dependents and exact participant
rosters, and a live policy. Owner refresh constructs at most one pending whole
delta; the exact run fetches and acknowledges it through MCP. A changed base
contract or unsafe or oversized incremental change requires a visible rebase. Acceptance must
prove pending/replay/restart, no-op cursors, messages/rosters with wrong-task
isolation, accepted/withdrawn/disputed knowledge and eligible closure re-offer,
reverse dependents, exact frozen-tool denial with no invented state, and byte/event
bounds without a provider or network.

Portable v1 moves exact project knowledge/applicability/contradiction snapshots
under a new local-owner attestation; it deliberately does not replay origin
authority or operational entities. Native provider resume, active-turn steering,
app-server ownership, remote users, and broader organizational authority remain
outside this milestone. Every completed capability scenario remains required.

No upstream repository should be created until the owner explicitly requests it.
