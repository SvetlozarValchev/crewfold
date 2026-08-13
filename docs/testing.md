# Testing strategy

## Objective

Crewfold controls long-running processes and durable state across unreliable
boundaries. Its tests must identify whether a failure belongs to the domain, store,
daemon transport, scheduler, runtime, provider, or external tool. A large end-to-end
test alone cannot provide that diagnosis.

The end-state suite must also prove management comprehension: after more work than
one person can reasonably inspect, the public product still explains accepted
delivery, rationale, verification, risk, unknowns, and required decisions without
requiring session-by-session reconstruction.

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
- Herdr driver plus recorded CLI/schema/session responses;
- Codex adapter plus recorded version/help/auth and lifecycle responses;
- Claude adapter plus recorded version/help/auth and lifecycle responses;
- Git observer plus real temporary repositories.

Each component suite labels its boundary in errors and exposes operation IDs.

The deterministic retrieval component suite uses the real pinned SQLite FTS5
engine. Ranking fixtures vary one tuple axis at a time and assert the exact final
revision-ID order; scope and authority tests prove ineligible rows never enter the
ranked set. Index failure tests remove or invalidate only the derived projection,
then prove canonical reads remain byte-stable before an explicit rebuild.

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

### 8. Management-comprehension acceptance

Exercise a deterministic project with at least ten concurrent agent/task histories,
mixed assessment states and conclusions, decisions, checks, reviews, risks,
duplicated work, and a deliberate contradiction. Provider transcripts are absent
from the fixture.

The scenario asserts that Crewfold:

- distinguishes agent activity and completed runs from accepted outcomes;
- explains material rationale through decision and constraint records;
- distinguishes self-report, mechanical checks, independent review, and accepted
  verification, including freshness;
- never turns missing, stale, or disputed evidence into a reliability claim;
- reports duplicated and contradictory work with its resolution state;
- produces a bounded, stable project briefing and a deterministic change view
  since an owner checkpoint;
- identifies the few remaining risks, unknowns, and owner decisions;
- drills each material aggregate claim into its durable provenance.

Narrative wording may receive tolerant rendering tests. The structured briefing
contract, claim identifiers, evidence classifications, and checkpoint diff are
strictly asserted.

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

A hidden provider-free mode of the built Crewfold binary for direct-runtime tests,
plus a generic MCP-client mode reusable by later Herdr tests. The legacy worker
emits structured reports for compatibility; `fixture-mcp` instead reads its scoped
briefing and sends normalized reports/artifacts through MCP. Both expose assigned
working directory and environment names for safety assertions, support
deterministic exit/timeout/signal behavior, and never call a model provider.

### Fixture Git repository

Generated in a temporary directory with known commits, branches, adjacent
standalone clones, a linked worktree, dirty state, and test commands. It contains
no network remote. M3 hashes fixture contents before and after inspection to prove
that observation does not mutate source or Git metadata.

### Fake/recorded Herdr endpoint

The implemented stateful endpoint exposes the documented CLI response shapes,
hosts the real Crewfold pane supervisor/fixture children, and covers compatible
and incompatible schemas, workspace creation, snapshots, process info, input,
read, attach, and close. Component fixtures add moved-pane, missing-pane, and
connection-loss responses. The opt-in dedicated-session test verifies those
assumptions against an installed Herdr without a model provider.

### Recorded Codex endpoint

The checked-in endpoint implements the exact no-model commands used by the
provider probe and accepts the real Codex launch manifest. During a run it starts
Crewfold's actual STDIO MCP bridge, reads the scoped briefing, submits a completion
proposal, and emits bounded JSONL lifecycle events. Authentication failure is a
separate recorded mode. This proves adapter wiring and failure attribution without
credentials, network access, or inference.

The real canary has a second acknowledgement gate because it consumes network and
provider usage:

```sh
CREWFOLD_LIVE_CODEX=1 CREWFOLD_ALLOW_MODEL_CALLS=1 ./test/live/codex/run.sh
```

It creates its own Git repository and Herdr session, permits one exact file
change, runs one local check, verifies the diff and commit count, and never
configures a remote. Without both flags it cannot call a model. The test enables
Codex child-command network access because some nested Linux environments cannot
construct Codex's isolated network namespace; workspace filesystem isolation
remains enabled and the task itself forbids network use.

On a Linux host where bubblewrap cannot construct its namespace, the canary can
instead put the whole Codex process inside an independently enforced container:

```sh
./test/live/codex/build-container.sh
CREWFOLD_CODEX_BINARY="$PWD/test/live/codex/containerized-cli.sh" \
CREWFOLD_LIVE_CODEX_SANDBOX=danger-full-access \
CREWFOLD_EXTERNAL_CODEX_SANDBOX=1 \
CREWFOLD_LIVE_CODEX=1 CREWFOLD_ALLOW_MODEL_CALLS=1 \
  ./test/live/codex/run.sh
```

The pinned container definition contains the installed Codex executable, its
matching `codex-code-mode-host` companion, and only the runtime libraries, CA
roots, and Git needed by the live task. The build helper copies no auth or
configuration. At runtime the wrapper copies only `auth.json` and the installation
identifier into an owner-private temporary `CODEX_HOME`, so
Codex may create ephemeral app-server state without modifying the real home. The
wrapper refuses to pull images itself, uses a read-only root with no Linux
capabilities, and mounts only that temporary auth/state directory plus the
disposable canary directory read-write. The outer
container is the security boundary; the inner Codex sandbox is disabled because
nesting it is exactly what the host cannot support. The wrapper closes container
stdin because the prompt is passed as an argument; otherwise Docker would expose
Herdr's persistent PTY as a pipe and `codex exec` would wait for an impossible
end-of-file before starting.

### Recorded Claude endpoint and provider-switch proof

The checked-in Claude Code endpoint implements the no-model version, help, and
authentication commands used by the provider probe, validates the strict one-shot
launch manifest, starts the real Crewfold STDIO bridge, reads the scoped briefing,
and proposes completion through MCP. Recorded missing-authentication, unsupported-
major, MCP-startup, and permission-boundary responses make failures attributable
without credentials, network access, or inference.

The black-box scenario is also the side-by-side portability proof. A recorded
Codex run writes a durable handoff to the stopped Claude agent. A separate Claude
run then receives the message through its immutable Crewfold briefing and
completes the dependent task. The scenario asserts that provider-private session
identifiers never cross logs or context and that raw transcripts remain excluded.

### Participant-bound cross-project fixture

The provider-free cross-project scenario registers adjacent `plugandrev` and
`engine-sim-offline` repositories as separate projects. The owner binds the exact
application and library tasks into one participant thread. A recorded engine
fixture sends while the plug fixture is offline; after daemon restart the exact
plug task sees the bounded inbox summary, reads, acknowledges, and replies through
the ordinary MCP mailbox tools. The surviving engine run then reads and
acknowledges that reply before both complete. A second run for another plug task
proves that agent identity alone cannot receive or wake for the message.

The same scenario proves ordinary direct mail remains invisible across projects,
invites one third-project participant with optimistic roster revision, keeps every
message single-recipient, and verifies message origins. Before/after projections
prove collaboration does not fabricate knowledge, dependencies, claims, or
meetings. No provider account, model call, remote, or shared Git ancestry is used.

The real Claude canary has an independent two-flag acknowledgement gate:

```sh
CREWFOLD_LIVE_CLAUDE=1 CREWFOLD_ALLOW_MODEL_CALLS=1 ./test/live/claude/run.sh
```

It uses a dedicated Herdr session and disposable one-file Git repository, caps the
provider run at `1.00` USD by default, verifies the exact diff and checks, and has
no remote. Without both flags it cannot call a model. Native Claude sandboxing is
enabled and configured to fail closed.

If the host cannot construct the nested Claude sandbox, the explicit outer
container route is:

```sh
./test/live/claude/build-container.sh
CREWFOLD_CLAUDE_BINARY="$PWD/test/live/claude/containerized-cli.sh" \
CREWFOLD_EXTERNAL_CLAUDE_SANDBOX=1 \
CREWFOLD_LIVE_CLAUDE=1 CREWFOLD_ALLOW_MODEL_CALLS=1 \
  ./test/live/claude/run.sh
```

The digest-pinned image contains the installed native Claude executable plus only
CA roots and Git. The build copies no authentication or configuration. At runtime
the wrapper copies only `.credentials.json` into an owner-private disposable
configuration directory, uses a read-only root, runs as the current non-root UID,
drops every Linux capability, and mounts only that temporary provider state and
the canary scope. The inner sandbox is disabled only after the separate external-
sandbox assertion because the container is then the enforcement boundary. The
wrapper refuses to pull an image and validates that the working directory and MCP
capability both belong to the disposable canary scope.
External-container mode places that scope under the user cache directory by
default so confined Docker installations can bind-mount it; an operator may set
`CREWFOLD_LIVE_CLAUDE_TEMP_ROOT` to another existing Docker-visible parent.

### Management workload fixture

A provider-free fixture represents enough parallel work that transcript review is
not a valid acceptance technique. It contains achieved, partial, not-achieved,
and unknown conclusions plus rejected and superseded assessment revisions;
material decisions; compatible and breaking changes; fresh and stale checks;
independent and self-reported evidence; unresolved risk; duplicate tasks; and
contradictory findings. Stable IDs and event cursors allow briefing and “since
checkpoint” assertions without model-dependent prose.

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
│  ├─ deterministic-execution/
│  ├─ direct-runtime/
│  ├─ scoped-mcp/
│  ├─ agent-messaging/
│  ├─ herdr-runtime/
│  ├─ codex-provider/
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
| Knowledge | contradiction, stale item, missing/corrupt search index, budget overflow |
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

The complete implemented offline gate is:

```sh
./scripts/check.sh
```

It runs formatting, vet, all package tests, the race suite when supported, and
twelve built-binary scenarios across local API, direct, Herdr, recorded Codex, and
recorded Claude boundaries. The direct messaging
scenario uses only public CLI/MCP surfaces, stops and restarts the daemon after an
offline send, compares inbox JSON byte-for-byte across restart, then has agents in
adjacent standalone clones read, acknowledge, reply, and complete. It also proves
forbidden recipients, oversized bodies, idempotency, bounded packet summary, and
visible wake failure without message loss.
The Herdr variant repeats the same two-agent flow using isolated recorded Herdr
surfaces, proves a successful prompt wake and native attach, and rejects an
incompatible installed schema before launch.

Preserve these conceptual future tiers as the suite expands:

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
- clear distinction between accepted intent and completed effect;
- clear distinction between activity, proposed completion, and accepted outcome;
- provenance, authority, strength, and freshness for management-level claims.

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

Any milestone that changes outcome, decision, evidence, risk, or briefing behavior
must also preserve the management-comprehension fixture. By the operator alpha,
the project briefing must answer what changed, why, how much to trust it, what
remains, and what needs the owner without reading transcripts.

If any item is waived, the milestone remains incomplete unless the waiver is an
explicit accepted decision with a follow-up gate.
