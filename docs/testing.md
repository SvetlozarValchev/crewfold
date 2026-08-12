# Testing strategy

## Objective

Crewfold controls long-running processes and durable state across unreliable
boundaries. Its tests must identify whether a failure belongs to the domain, store,
daemon transport, scheduler, runtime, provider, or external tool. A large end-to-end
test alone cannot provide that diagnosis.

## Test layers

### 1. Domain unit tests

Pure tests for commands, state transitions, policy decisions, conflict rules,
ranking, and scheduling. They use controlled clocks and IDs but no database,
filesystem, process, or network.

These tests answer: “Given these facts, is this transition valid and which events
should result?”

### 2. Store and migration tests

Run against real temporary SQLite databases. They verify transactions, constraints,
idempotency, event/projection agreement, queue leases, backup, and migrations.

Every released schema version gets a small checked-in database fixture containing
representative data. Upgrade tests open a copy, migrate it, and validate domain
queries. Tests never mutate the canonical fixture in place.

### 3. Protocol contract tests

Validate JSON envelopes, JSON Schemas, MCP tools, adapter capabilities, error
codes, version negotiation, unknown-field behavior, and compatibility fixtures.

Golden files are appropriate for stable structured contracts. Human table output
should use focused rendering assertions rather than brittle full-output snapshots.

### 4. Component integration tests

Exercise one real boundary at a time:

- daemon plus real Unix socket and SQLite store;
- real SQLite store with direct command handlers;
- direct runtime plus fixture worker;
- MCP server plus fixture client;
- Herdr driver plus recorded/fake socket responses;
- Git observer plus real temporary repositories.

Each component suite labels its boundary in errors and exposes operation IDs.

### 5. Black-box acceptance scenarios

Launch the built Crewfold binary with an isolated temporary home, explicit socket,
and fake/fixture adapters. Interact only through public CLI/API/MCP surfaces. These
tests prove milestone outcomes and persistence across actual process restarts.

Each scenario owns its paths and processes. Cleanup identifies exact process IDs
and temporary directories; it never deletes broad workspace or home paths.

### 6. Opt-in live conformance tests

Use installed Herdr, Codex, or Claude Code only when the operator explicitly opts
in and credentials are already configured. They run in disposable fixture
repositories with strict task scope, timeout, and cost bounds.

Live tests are canaries, not normal CI prerequisites. Offline fixtures remain the
primary regression suite because provider behavior, availability, and cost are
external variables.

### 7. Load and endurance tests

Simulate many durable agents/tasks/events with few or no paid runs. Measure status
query latency, scheduler latency, memory, database growth, queue lag, and startup
reconciliation. Short load tests run regularly; long soak tests run before beta
releases.

## Required test fixtures

### Controlled clock and ID source

Allows deterministic leases, timeouts, event ordering, and snapshots. Production
construction must make it difficult to select these accidentally.

### Fake provider

Consumes a scenario with ordered actions such as:

```yaml
steps:
  - report_progress: "started"
  - send_message:
      to: reviewer
      body: "please inspect artifact A"
  - wait_for_message:
      kind: review_response
  - propose_completion:
      evidence: [artifact-a]
```

It supports deterministic delay, block, malformed output, crash, duplicated call,
and ignored stop.

### Fake runtime

Models surfaces and process lifecycle without starting a process. It records every
operation and supports controlled failure before/after acknowledgement.

### Fixture worker

A tiny local executable for the direct and Herdr runtime tests. It can act as a
generic terminal process or MCP client and never calls a model provider.

### Fixture Git repository

Generated in a temporary directory with known commits, branches, adjacent
standalone clones, a linked worktree, dirty state, and test commands. It contains
no network remote. M3 hashes fixture contents before and after inspection to prove
that observation does not mutate source or Git metadata.

### Fake/recorded Herdr endpoint

Implements the tested schema responses and event sequences, including incompatible
versions, moved panes, stopped agents, and connection loss. Live Herdr tests verify
that the fixtures still represent reality.

## Milestone scenario layout

When implementation begins, acceptance assets should follow a discoverable shape:

```text
test/
├─ fixtures/
│  ├─ providers/
│  ├─ runtimes/
│  ├─ protocol/
│  └─ databases/
├─ scenarios/
│  ├─ daemon-api-spine/
│  ├─ persistent-workspace/
│  ├─ m05-fake-agent-loop/
│  └─ ...
└─ live/
   ├─ herdr/
   ├─ codex/
   └─ claude/
```

Each scenario contains:

- a short purpose and prerequisites;
- setup expressed through public commands where possible;
- the exact command sequence;
- structured assertions;
- owned resource/cleanup manifest;
- expected event types and final state;
- a representative failure variant.

## Failure-injection catalogue

The suite grows this matrix milestone by milestone:

| Boundary | Failures |
| --- | --- |
| Command/API | duplicate request, disconnect, stale revision, malformed payload |
| SQLite | busy timeout, failed migration, interrupted transaction, corrupt index |
| Queue | duplicate delivery, expired lease, worker crash, poisoned item |
| Runtime | launch timeout, orphan, unexpected exit, ignored stop, stale handle |
| Provider | missing binary, incompatible version, blocked UI, malformed result |
| MCP | expired capability, cross-run access, duplicate mutation, oversized body |
| Git | missing checkout, changed HEAD, dirty drift, command failure |
| Messaging | recipient offline, wake failure, duplicate ack, forbidden recipient |
| Meeting | participant timeout, facilitator crash, stale frozen context |
| Knowledge | contradiction, stale item, broken search index, budget overflow |
| Scheduler | capacity saturation, dependency cycle, claim race, restart mid-launch |

Fault injection should happen at named seams rather than through arbitrary sleeps.
Tests wait on observable barriers/events so they remain deterministic.

## Persistence and crash testing

For every durable saga, test daemon termination at these conceptual points:

```text
before intent commit
after intent commit, before effect
after effect, before acknowledgement commit
after acknowledgement commit
```

On restart, assert:

- committed intent is not lost;
- external effects are not duplicated;
- unknown outcomes remain explicitly unknown until reconciled;
- leases and capacity reservations converge;
- the event journal and projection agree;
- the operator can see what recovery did and why.

## Security and policy testing

Every new mutation states its required capability and receives tests for:

- allowed actor and scope;
- denied actor;
- correct actor but wrong project/task/run;
- action requiring approval;
- expired run capability;
- malicious or malformed repository/provider text;
- secret-like values in logs, messages, artifacts, and context packets.

Authorization is checked at the domain-command boundary even if a CLI or MCP tool
already filtered the action.

## Test suite commands

The exact build tool is chosen in M0, but preserve these conceptual tiers:

```sh
# Fast, deterministic, offline
go test ./...

# Built-binary scenarios with fake and fixture adapters
crewfold test acceptance

# Fault/restart scenarios
crewfold test fault

# Opt-in installed runtime
crewfold test live --runtime herdr

# Opt-in paid/network provider canaries
crewfold test live --provider codex
crewfold test live --provider claude

# Scale simulation without provider calls
crewfold test load --profile personal-100
```

No normal test command may silently invoke a paid provider or use credentials.

## Observability assertions

Tests should assert not only final state but also diagnostic quality:

- stable error code;
- operation ID or affected entity ID;
- relevant event sequence;
- human-readable reason;
- retryability and next action where meaningful;
- absence of secrets and unrelated paths;
- clear distinction between accepted intent and completed effect.

This prevents a system that is technically correct but impossible to debug.

## Flake policy

- No retry is used to hide an unexplained deterministic test failure.
- Timing-sensitive tests use controlled clocks and event barriers.
- Live-provider failures are classified as Crewfold regression, compatibility
  change, provider unavailability, authentication, or budget/timeout.
- A quarantined test needs an owner, issue/reason, and expiration condition.
- Repeated live canary instability must improve the adapter diagnosis; it must not
  weaken core acceptance gates.

## Definition of done for a milestone

A milestone is done only when:

- its checked-in acceptance scenario passes from a clean temporary home;
- unit, store, protocol, and relevant component tests pass;
- its representative fault produces the documented outcome;
- durable state survives the required restarts;
- `doctor`, logs, events, and JSON output make failures attributable;
- security/policy cases for new actions pass;
- public docs and schemas match behavior;
- deferred items are named;
- the repository is clean after the scenario;
- the milestone review packet is recorded.

If any item is waived, the milestone remains incomplete unless the waiver is an
explicit accepted decision with a follow-up gate.
