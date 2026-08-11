# Roadmap

## How to read this roadmap

This is the compact delivery sequence. The exact deliverables, acceptance
commands, failure cases, and exit gates are in the
[implementation plan](implementation-plan.md). The test infrastructure and quality
rules are in the [testing strategy](testing.md).

The current repository is in planning/bootstrap state. No implementation milestone
has started.

## Sequence

| Milestone | Increment | Demonstrable outcome | Depends on |
| --- | --- | --- | --- |
| M0 | Buildable repository | Build one binary; run version/help/self-check and local CI | Documentation baseline |
| M1 | Daemon/API spine | Start, query, diagnose, and cleanly stop a local daemon | M0 |
| M2 | Persistent workspace | Commit an event, restart, restore, and inspect the same workspace | M1 |
| M3 | Projects/checkouts | Register and observe a disposable Git repository without mutating it | M2 |
| M4 | Agents/tasks | Define durable roles, tasks, dependencies, leases, and readiness | M3 |
| M5 | Fake-agent loop | Assign, launch, progress, block, complete, and hand off one task | M4 |
| M6 | Direct runtime | Run and recover a real fixture subprocess with bounded output | M5 |
| M7 | MCP/briefing | Let a run read scoped context and report through authenticated MCP | M6 |
| M8 | Agent messaging | Exchange and acknowledge durable mail while one agent is offline | M7 |
| M9 | Herdr runtime | Run the fixture agent in Herdr and reconcile terminal lifecycle | M8 |
| M10 | Codex canary | Complete the proven task/MCP loop with one real Codex session | M9 |
| M11 | Claude canary | Complete the same loop with Claude and switch providers via handoff | M10 |
| M12 | Claims/overlap | Detect declared and observed conflicting work deterministically | M8, M9 |
| M13 | Meetings | Resolve a two-/three-agent overlap into durable task/claim changes | M12 |
| M14 | Canonical knowledge | Deliver explicitly accepted decisions/findings without transcripts | M13 |
| M15 | Curator/retrieval | Find, reconcile, and refresh relevant knowledge deterministically | M14 |
| M16 | Manager/supervisor | Propose work and advance dependencies under explainable policy | M15 |
| M17 | Local checks/CI watcher | Route fresh check evidence without granting merge authority | M16 |
| M18 | Operator TUI | Understand and intervene in the crew from one terminal dashboard | M17 |
| M19 | Personal beta | Back up, recover, upgrade, and load-test 100 registered roles | M18 |
| M20 | OSS release candidate | Install, demo, and extend Crewfold from a clean environment | M19 |

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

### Operator alpha — M18

One coherent terminal dashboard makes the active crew understandable and
intervenable without polling panes.

### Personal beta — M19

One developer can operate and recover the system at the target of roughly 100
registered roles with bounded active concurrency.

### Public release candidate — M20

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

The critical path remains sequential through M8. Small supporting work may happen
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

M0 is the only approved implementation target after this plan is accepted:

1. choose the current stable Go toolchain and minimal CLI dependency;
2. create the module and one `crewfold` entry point;
3. implement version/help/self-check;
4. establish offline unit/static checks;
5. add the first checked-in acceptance scenario and review record template.

M0 must not introduce SQLite, daemon code, provider SDKs, Herdr calls, MCP, or a
web/TUI framework.

No upstream repository should be created until the owner explicitly requests it.
