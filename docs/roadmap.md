# Roadmap

## How to read this roadmap

This is the compact delivery sequence. The exact deliverables, acceptance
commands, failure cases, and exit gates are in the
[implementation plan](implementation-plan.md). The test infrastructure and quality
rules are in the [testing strategy](testing.md).

Current status: **M0 through M13 are complete**. Evidence is recorded in the [M0
review](reviews/buildable-repository.md), [M1
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
retained as optional release/upgrade conformance.
M12's [claims and overlap review](reviews/claims-overlap.md) covers leased declared
scope, exact path-glob intersection witnesses, deterministic policy response,
restart-aware Git drift, and separate checkout attribution. Its implementation
commit is `f756d7c427a82f3661997ccacdbe94ab1d085b36`.
M13's [structured meeting review](reviews/structured-meetings.md) covers frozen
inputs, restart-safe independent positions, owner/reviewer/bounded-manager
authority, typed consolidation actions, human takeover, and the pinned `sqlc`
persistence boundary. Its implementation commit is
`432e6e02a85c4793ec26f08bfcfc7783a587d04d`.

## Sequence

| Milestone | Increment | Demonstrable outcome | Depends on |
| --- | --- | --- | --- |
| M0 ✓ | Buildable repository | Build one binary; run version/help/self-check and local CI | Documentation baseline |
| M1 ✓ | Daemon/API spine | Start, query, diagnose, and cleanly stop a local daemon | M0 |
| M2 ✓ | Persistent workspace | Commit an event, restart, restore, and inspect the same workspace | M1 |
| M3 ✓ | Projects/checkouts | Register and observe a disposable Git repository without mutating it | M2 |
| M4 ✓ | Agents/tasks | Define durable roles, tasks, dependencies, leases, and readiness | M3 |
| M5 ✓ | Fake-agent loop | Assign, launch, progress, block, complete, and hand off one task | M4 |
| M6 ✓ | Direct runtime | Run and recover a real fixture subprocess with bounded output | M5 |
| M7 ✓ | MCP/briefing | Let a run read scoped context and report through authenticated MCP | M6 |
| M8 ✓ | Agent messaging | Exchange and acknowledge durable mail while one agent is offline | M7 |
| M9 ✓ | Herdr runtime | Run the fixture agent in Herdr and reconcile terminal lifecycle | M8 |
| M10 ✓ | Codex canary | Complete the scoped MCP loop with a real disposable Codex session | M9 |
| M11 ✓ | Claude portability | Complete the same recorded loop with Claude and switch providers via handoff | M10 |
| M12 ✓ | Claims/overlap | Detect declared and observed conflicting work deterministically | M8, M9 |
| M13 ✓ | Meetings | Resolve a two-/three-agent overlap into durable task/claim changes | M12 |
| M14 | Canonical knowledge | Deliver explicitly accepted decisions/findings without transcripts | M13 |
| M15 | Curator/retrieval | Find, reconcile, and refresh relevant knowledge deterministically | M14 |
| M16 | Manager/supervisor | Propose work and advance dependencies under explainable policy | M15 |
| M17 | Local checks/CI watcher | Route fresh check evidence without granting merge authority | M16 |
| M18 | Outcome briefings | Explain accepted delivery, rationale, evidence, risk, and owner decisions | M17 |
| M19 | Operator TUI | Understand and intervene in the crew from one terminal dashboard | M18 |
| M20 | Personal beta | Back up, recover, upgrade, and load-test 100 registered roles | M19 |
| M21 | OSS release candidate | Install, demo, and extend Crewfold from a clean environment | M20 |

## Capability ladder

```text
buildable binary
  -> observable daemon
  -> durable state
  -> repository awareness
  -> tasks and roles
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
  -> local checks and CI watching
  -> outcome ledger and management briefings
  -> operator TUI
  -> personal-scale recovery
  -> public release readiness
```

Each arrow is a release boundary. Later milestones may not redefine earlier domain
semantics merely to make an integration easier; they must pass the earlier
acceptance scenarios unchanged or deliberately migrate their contracts.

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

Agents resume from canonical knowledge; a curator and deterministic search keep it
relevant without making retrieval authoritative.

### Personal alpha — M16 through M17

Managers propose work, dependencies advance under policy, and local checks route
fresh evidence to the right roles.

### Management alpha — M18

Structured outcome assessments and evidence-backed project briefings let the
owner understand delivery, rationale, reliability, risk, unknowns, and required
decisions without reconstructing individual sessions.

### Operator alpha — M19

One coherent terminal dashboard makes the active crew understandable and
intervenable without polling panes.

### Personal beta — M20

One developer can operate and recover the system at the target of roughly 100
registered roles with bounded active concurrency.

### Public release candidate — M21

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
- browser console as a prerequisite for basic operation;
- autonomous company hierarchy.

The local node and protocols retain extension seams for those later capabilities,
but they do not carry their implementation cost now.

## Immediate next milestone

M14 is next: preserve explicitly proposed and accepted decisions/findings as
canonical, provenance-linked knowledge and deliver them to replacement agents
without copying transcripts. Meeting resolutions provide the first decision
source, but retrieval cannot make content authoritative.

Native provider resume, active-turn steering, app-server ownership, remote users,
and automatic source integration remain outside M14. Every completed capability
scenario remains required.

No upstream repository should be created until the owner explicitly requests it.
