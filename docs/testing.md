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

### 2. Store and schema tests

Run against real temporary SQLite databases. They verify transactions, constraints,
idempotency, event/projection agreement, queue leases, backup, and exact creation
of the current schema baseline. Canonical-integrity tests open fresh databases,
exercise representative data, and reject malformed or partial authority graphs.

### 3. Protocol contract tests

Validate JSON envelopes, JSON Schemas, MCP tools, adapter capabilities, error
codes, exact current-version selection, unknown-field behavior, and current conformance fixtures.

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

The structured briefing contract, claim identifiers, evidence classifications,
and checkpoint diff are strictly asserted. There is no narrative-rendering test
or alternate briefing representation.

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
plus a generic MCP-client mode reusable by later Herdr tests. `fixture-mcp` reads
its scoped briefing and sends normalized reports/artifacts through MCP. It exposes
assigned working-directory and environment names for safety assertions, supports
deterministic exit/timeout/signal behavior, and never calls a model provider.

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

### Bounded curator fixture

The provider-free curator scenario creates eleven accepted structured meeting
resolutions plus one authenticated agent proposal labeled `high` and `verified`.
It proves derive-only processing, disabled-rule queueing, restart-stable reads,
persisted rule-revision inspection, explicit owner enablement, the ten-acceptance
pass bound, exact authority and derivation links, and idempotent replay. The agent
fixture can propose through the real scoped MCP tool but has no fields for actor,
project, run, or source and its reserved acceptance probe must stop at
`run.tool_denied`.

A valid accepted meeting with a 2049-byte summary proves the safe-copy rule never
truncates. Every fresh process evaluation returns its exact proposal revision with
the stable `summary_not_exact_safe_copy` skip reason while creating no knowledge,
derivation, authority, or curator event. Store mutation-hook tests inject failure
after projection and event writes because public commands deliberately expose no
transaction-failure switch. Store fixtures with 101 structured sources prove the
100-evaluation bound and deterministic follow-up progress; starvation regressions
keep pre-existing safe proposals and later valid sources ahead of repeating
invalid-source skips.

### Owner-confirmed contradiction fixture

The provider-free contradiction scenario renders a strict JSON template only
after two dynamic exact revision IDs exist. Its authenticated `fixture-mcp` run
reports one project-wide/task-scoped pair, asserts receipt, retries the reversed
pair with the same key, and probes the reserved confirmation name. The fixture
schema exposes only `left_revision`, `right_revision`, and `reason`; it has no
actor, run, workspace, project, task, status, or governance override. Explicit
`report_received` and `confirm_denied` assertions are required.

Through public commands, the scenario proves proposed records have no effect;
owner confirmation creates conservative whole-revision quarantine; project-only
and unrelated-task search exclude the broad participant before `LIMIT`; and an
otherwise eligible explicit context build fails with `knowledge_conflict` while
appending no packet, event, or idempotency result. A pre-confirmation packet stays
byte-stable. Dismissal restores search and lets the exact failed context key
succeed, while the canonical terminal pair cannot be re-reported. Detail and
owner-decision replay survive daemon restart.

The unique report-reason sentinel must appear in canonical contradiction detail
but remain absent from captured provider logs. Store/schema tests additionally
cover multi-conflict last-open behavior, stale/supersede automatic resolution,
more-than-200 dispute/authority histories, first-16 context error disclosure,
confirm-time revalidation, denied non-owner governance, direct-SQL trigger
attacks, mutation-hook rollback, and run-report replay after completion/restart.
No model, credential, remote, or network is used.

### Portable project knowledge fixture

The provider-free portable scenario builds one project containing broad and
task-scoped canonical histories across proposed, rejected, current, stale, and
superseded states plus proposed/open/dismissed/resolved contradictions. It exports
twice to distinct private directories and requires exact manifest/Markdown bytes,
the two-file listing, and `0700`/`0600` modes.

It imports into a fresh daemon with exact `--create-scope` and the expected full
digest, then proves exact IDs, bodies, states, ordered sources, portable task
applicability, and contradiction effects survive restart and immediate re-export.
No operational task, meeting, agent, run, repository, or checkout may appear.
Project-only search cannot expose task-scoped imported knowledge; an imported open
contradiction remains a fail-closed context/search gate. Export remains available
with missing/degraded FTS and explicit rebuild restores derived search.
Imported knowledge/contradiction detail contains no forged origin authority
checks; the target journal contains only local-owner import attestations.

Same- and new-key replay append no event. Unsafe paths, extra/missing files,
tampered hashes/rendering, scope mismatch, absent anchors without create-scope,
and a nonempty target all fail before a canonical row, receipt, event, or
idempotency result is visible. The scenario compares durable table-count
fingerprints as well as journal bytes around those failures. Store mutation hooks
cover transaction rollback and restart recovery. The scenario uses no provider,
model, credential, network, or `jq`.

### Live context delta fixture

The provider-free `live-context-deltas` scenario builds the binary, isolated
SQLite state, temporary Git projects, and `fixture-mcp` runs. It makes no model,
credential, live provider, remote, or network call. Its dynamic scenario JSON may
name IDs created through the public CLI, but it exposes no workspace/project/task/
run/cursor authority to MCP. The hidden fixture can only fetch its own pending
delta, inspect typed fields, acknowledge the exact ID/sequence, repeat the same
acknowledgement, or assert immutable-policy denial.

The acceptance matrix is split at observable boundaries:

| Contract | Provider-neutral proof |
| --- | --- |
| Current packet base | CLI JSON freezes nonzero `as_of_event_sequence`, direct dependencies, bounded reverse dependents, exact participant rosters, collaboration budget, live policy, and the two live MCP tool names |
| Explicit creation | accepting an applicable decision alone creates no delta; one owner `context refresh` creates exactly one `knowledge_accepted` change |
| Pending/idempotency | a second refresh key before acknowledgement returns the same delta/sequence/hash and appends no second build event |
| Restart | stop/restart the daemon while pending; local show/list and the exact run fetch return the byte-identical delta |
| Consumption authority | `fixture-mcp` fetches with `{}`, acknowledges exact ID/sequence, and replays the same receipt; another run/task sees `none_pending` and cannot acknowledge the target ID |
| No-op cursor | after acknowledgement, a refresh with no eligible change returns `up_to_date`, advances inspected-through to the current cutoff, and appends no delta or journal event |
| Message and roster scope | owner-created/invited participant state produces a whole roster and bounded message preview for the exact participant task; no full body is in the delta and another task of the same agent remains unaffected |
| Contradiction/withdrawal/re-offer | owner confirmation produces open/quarantine plus withdrawal of delivered participants; an accepted applicable decision hidden before delivery receives a no-body disputed suppression tombstone; after final closure, eligible decisions from either exact category are re-offered with cause `contradiction_closed_reoffer`, while an open contradiction snapshot alone grants no re-offer and findings/otherwise-ineligible decisions remain absent |
| Reverse dependent | a legal new same-project task depending on the run task appears as one whole `dependent_added`; it grants no task/cross-project authority |
| Bounds/failures | store/protocol tests prove the 1,000-event, 16 KiB delta, 64 KiB chain, one-pending, strict-shape, unsupported-event, direct-dependency drift, rollback, event-link, and cross-run trigger gates; the black box asserts at least one public rebase/failure result |

The fixture waits on public status/delta observations rather than assuming daemon
or provider timing. Any shell delay exists only as a bounded startup barrier and
is paired with observable polling. Failure diagnostics retain exact JSON/event
files inside the owned temporary directory until cleanup prints them.

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

The provider-free `outcome-briefings` scenario creates ten task histories and an
immutable pre-work commitment for each without a provider transcript. It covers
accepted achieved and partial judgments, a proposal that remains unaccepted, a
rejected unknown judgment, an accepted successor that atomically supersedes the
old current judgment, unresolved risk/unknown state, restart, and an exact project
checkpoint. Public CLI assertions bound claims/bytes, preserve older current
diagnoses in the checkpoint view, restrict change-history claims to the exclusive
lower bound, drill one claim through exact provenance, and prove show/explain emit
no event.

Store and protocol fixtures add not-achieved conclusions, material decisions,
compatible and breaking effects, fresh/stale mechanical checks, exact independent
review provenance, duplicate work, contradictions, omission fairness, and fault
barriers. Stable IDs and event cursors allow every briefing assertion without
model-dependent prose.

### Operator TUI fixture

The provider-free `operator-tui` scenario starts a real isolated daemon and the
built `crewfold ui` client over its owner-only socket. Canonical fixture records
provide one complete M18 briefing, one attachable recorded Herdr run, and one
owner intervention. The script drives Briefing and Work through a real 180x40
monochrome pseudo-terminal; it does not use a static renderer, hidden product
mode, SQLite query, provider transcript, or model call. Package fixtures cover
every screen and the urgent-aggregate exact drill-down sets without weakening the
public script's real terminal boundary.

Package fixtures separately reduce deterministic messages through the real model
and render empty, normal, disconnected/stale, capped, and large views. Hostile
strings include invalid UTF-8, CSI/OSC escape attempts, every disallowed C0/C1
class, bidirectional isolates/overrides, combining/wide characters, and excessive
length. Cursor fixtures cover exactly 1,000 events, more than 10,000 events,
duplicates, gaps, nonmonotonic order, unknown kinds, malformed envelopes, daemon
rewind, response overflow, and timeouts at bootstrap, polling, refresh, action,
and attach boundaries. A controlled slow/failing section fixture records active
requests and generation IDs: no more than four non-event reads overlap, newer
generations discard every stale section result, and the applied cursor waits for
all sections invalidated through the candidate. A mutation committed midway
through the controlled batch raises the final event-head fence; that mixed batch
is never applied or rendered as live.

The scenario compares the complete event page byte-for-byte immediately before
the UI and after navigation, briefing inspection, and ordinary attach. It then
kills and restarts the daemon while the same UI remains open, observes stale then
live state with the same selected run, and confirms one resume that produces
exactly one `run.resumed` event. Store, client, transport, and reducer fixtures
separately prove project resolution by exact ID and name is pure, bootstrap and
manual refresh append no event, and a lost mutation response replays the exact
frozen request/idempotency key to one committed result. Arbitrary agent role and
launch-profile purpose strings are authority-looking in those fixtures and have
no effect on ordering, urgency, action availability, or permission.

### Personal-scale recovery fixture

`crewfold test load --profile personal-100` owns a new private temporary data
directory and accepts no caller data directory, checkout, provider home, or
credential path. It makes no model, paid-provider, network, or external
repository call. The fixture contains exactly one workspace, ten projects, 100
arbitrary-role agent definitions, 1,000 tasks, and 100,000 known current-contract
events. One project owns exactly 80,000 events; every other project has an urgent
owner decision and mixed outcome/evidence state. A saturated phase reserves eight
unresolved runs with no more than two starting, then deterministically settles
them before recovery checks.

The report records OS, architecture, kernel, Go and SQLite versions, CPU, logical
CPU count, memory, repetitions, p50/p95/p99/max, bytes, peak RSS, goroutines, and
open file descriptors. It proves the existing urgency-banded, per-project
round-robin workspace briefing cannot let the noisy project crowd every unrelated
decision out. Project and workspace briefings remain at most 128 whole claims and
64 KiB.

The recovery scenario creates a quiescent online SQLite snapshot while later
source writes continue, binds exactly the check and run-log artifacts referenced
by that cut, verifies it after deleting the source daemon/database, restores it
to a nonexistent directory, and proves a pending restore cannot start. After the
source is retired, explicit activation generates a distinct node key, no
capabilities, no live bindings, and no coordination event. Store and process
fixtures kill create/restore at every publish barrier, hold SQLite busy, corrupt
each manifest/database/artifact class, inject nonterminal restored state, and
exercise owner-confirmed lost-run retirement.

### Manager/supervisor authority fixture

The provider-free `manager-supervisor` scenario uses a fixture grantee with an
exact current-packet management grant,
two change-producing agents, one independent evidence agent, owner-defined launch
profiles, and bounded global/project/provider/agent policy. Role strings are
deliberately arbitrary: an otherwise equivalent agent with the same role label but
no grant is denied. The grantee submits one `A -> B -> review` plan through the
real scoped MCP surface. Public reads prove submission creates no task, dependency,
assignment, claim, context, run, or capacity reservation; one owner acceptance
applies the complete plan.

The scenario then proves dependency completion schedules B exactly once, one
policy-external recommendation creates exactly one approval request, and an owner
decision is neither bypassed nor replayed as a second effect. Restart after durable
scheduling intent but before worker launch recovers the same action and run. The
fixture makes no provider, model, credential, remote, or network call.

Store/domain tests cover cycles; cross-project/objective scope; finite and
unlimited budget semantics; disabled/stale or wrong-agent profile revisions;
claim conflict; global, project, provider, agent, and checkout contention; stale
or lost runs before reassignment; concurrent scans; raw-SQL corruption; transaction
barrier rollback; and current-packet no-grant denial versus exact grant/revocation.
Every supervisor condition has a named executable case: `dependency_ready`
auto-applies under the default policy, while `blocked`, `stale`, `failed`,
wall-time `over_budget`, and `repeated_failure` remain inert and create exactly one
approval request unless an exact bounded owner policy revision permits the response.
Queue-boundary cases pin priority/readiness/ID order, readiness timestamps derived
from real ready/dependency facts, 30-second stable deferral, relevant-fact early
wake, and non-starvation past deferred heads. Intent cases pin every definitive
terminal outcome, policy-bounded start-failure retention, owner cancellation, and
manual-assignment exclusion. Worker-authority cases prove that the exact committed
scheduling/retry receipt survives later profile retirement, agent
disablement or revision change, and assignment-deadline passage for that one
launch while a new placement must revalidate current authority.

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
| SQLite | busy timeout, failed baseline initialization, interrupted transaction, corrupt index |
| Queue | duplicate delivery, expired lease, worker crash, poisoned item |
| Runtime | launch timeout, orphan, unexpected exit, ignored stop, stale handle |
| Provider | missing binary, incompatible version, blocked UI, malformed result |
| MCP | expired capability, cross-run access, duplicate mutation, oversized body |
| Git | missing checkout, changed HEAD, dirty drift, command failure |
| Messaging | recipient offline, wake failure, duplicate ack, forbidden recipient |
| Meeting | participant timeout, facilitator crash, stale frozen context |
| Knowledge/context | contradiction, stale item, missing/corrupt search index, curator rollback, oversized safe-copy source, packet/delta/chain/event-window overflow, unsafe base, unsupported change, pending replay |
| Scheduler | proposal cycle/scope/budget/profile rejection, global/project/provider/agent/checkout saturation, claim race, concurrent scan, stale or lost run, restart after intent before launch, bounded journal catch-up/restart, unknown-event fail-closed, public no-op replay |
| Manager/supervisor authority | current packet without grant denied, exact grant/revocation, same-role ungranted denial, self-accept denial, inert proposal, stale owner decision, approval replay, raw-SQL detached receipt/hash/source |
| Local checks | fixed-spec replay mismatch, launch/terminal dirty or changed HEAD, timeout, excessive output, vanished supervisor, unavailable Git observation, wrong recipient, orphan artifact, daemon restart while running |
| Check-watch authority | current packet without grant denied, exact grant/revocation, management/check-watch grant mutual exclusion, same-role ungranted denial, scope/command/checkout injection, forged evidence class/subsystem message, inert repair proposal |
| Backup | post-cut writes, nonquiescent run/check/intent/action/wake, missing or changed artifact, disk full, cancellation, daemon kill, lost response, unsafe path/mode/type/extra file |
| Restore | unavailable source, existing target, partial copy, pending startup, unretired parallel source, changed activation digest, injected nonterminal state/binding, fresh-node-key failure |
| Personal load | noisy project, saturated provider/starting slots, SQLite writer, unavailable provider, truncated output, repeated kill/restart, resource and latency ceiling |

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

For manager/supervisor mutations, tests also prove proposal authorship is distinct
from owner acceptance, manager and supervisor cannot decide approvals, historical
packet versions never gain tools, revocation is checked on each proposal call, and
agent/profile role or purpose text cannot select launch parameters. A same-UID
process with arbitrary database and node-key write access remains outside the
authentication threat model; raw-SQL tests instead prove fail-closed constraints
and read validation for partial or internally detached histories.

For M17, tests additionally prove that `AgentDefinition.Role` and
`LaunchProfile.Purpose` are never authorization, watcher-selection, routing,
repair, or evidence inputs. Check definitions have no stdin/environment/shell
escape surface. The dedicated check child lacks provider and MCP configuration.
Lifecycle inspection exposes no text, and only the bounded redacted logs path may
become an artifact.

A clean equal launch/terminal HEAD is required for verification. Dirty checks may
run only as unknown-freshness diagnostics. A later changed HEAD or dirty
observation is monotonically stale; returning to an old HEAD cannot revive it.
The Git observer used by a watch pass is fresh and real rather than only the
checkout projection's cached observation time.

Check-result, freshness, evidence, route, subsystem-message, and repair tests must
prove task state, completion acceptance, Git commit/push/merge, deployment, and
integration order are unchanged. Direct local checks use a trusted
owner-authored executable boundary, not a sandbox claim; the suite does not infer
no-network/no-Git confinement from direct execution.

For M20, bundles and restore targets are owner-private and every diagnostic is
checked for node keys, capability material, opaque handles, provider-home values,
log contents, and unrelated paths. The exact database remains sensitive and is
not redacted: it legitimately contains messages, evidence, and registered
checkout paths. Manifest and content hashes prove corruption or inconsistent
copy, not authenticity against a malicious same-UID process that can rewrite
both.

## M17 executable acceptance and security matrix

The following IDs and results are stable. Every row is exercised through the
lowest relevant store/runtime test and at least one public daemon/CLI/MCP scenario
where a public surface exists.

| ID | Adversarial setup | Required observable result |
| --- | --- | --- |
| `M17-AUTH-01` | same arbitrary Role on two agents, one exact grant | only the exactly granted run advertises/calls tools |
| `M17-AUTH-02` | Role/Purpose renamed to watcher/manager/integrator | authority/routing unchanged; no query checks those fields |
| `M17-AUTH-03` | Current packet without a grant, revoked/expired/stale grant, wrong project/definition/revision, scope/command/checkout injection | denied+audited, no check rows/effect |
| `M17-AUTH-04` | check grant and manager grant cannot coexist in the current packet; reserved repair acceptance denied | packet/call is denied; no delegated or owner effect |
| `M17-DEF-01` | owner definition freezes exact executable/argv/workdir/timeout/cap; agent cannot inject env/stdin/shell args; retirement blocks new run but not receipted recovery | only the frozen definition executes; no new post-retirement request is accepted |
| `M17-RUN-01` | clean HEAD pass | exactly named criterion `verified`; no other criterion/task status changes |
| `M17-RUN-02` | trusted nonzero/signal | failed result+mechanical evidence and exactly correct current task-owner subsystem message |
| `M17-RUN-03` | timeout/excess output/crash | one outcome plus bounded redacted stdout/stderr/diagnostic artifact with omitted counts/hash |
| `M17-EVID-01` | caller/raw SQL attempts to label check as independent/policy acceptance or make stale/missing verified | rejected/read fails closed |
| `M17-FRESH-01` | HEAD change or dirty observation | one monotonic stale revision; return to old HEAD does not revive; rerun needed |
| `M17-FRESH-02` | dirty-boundary/unavailable/missing result | visible unknown/missing and never verified, even exit 0 |
| `M17-ROUTE-01` | owner/evidence/coordination recipients have arbitrary identical role strings | exact assignment/route receipts select only intended agents; no active owner records unroutable |
| `M17-MSG-01` | raw/agent attempt to forge subsystem/owner provenance or detach result/recipient | constraint/read rejection |
| `M17-REPAIR-01` | failure with disabled/no-op grant | no proposal; enabled exact policy+granted op creates one inert proposal; only owner accept creates one exact-profile repair task/intent; replay/stale pass no duplicate |
| `M17-REC-01` | inject at every DB barrier | wholly absent or wholly committed/replayable; orphan job/result/message/evidence is never executable/read as valid |
| `M17-REC-02` | daemon stop while child runs | stable spec/ID reconciles same child and records exactly one result, or explicit unknown, never second execution |
| `M17-REC-03` | tamper launch.json/effective replay spec/handle/output blob | conflict or unknown/read failure, never trusted pass |
| `M17-WATCH-01` | >100 facts/restart/unknown event | cursor pages restart-safely, no action across unknown; background no-op emits nothing, public no-op replay returns one receipt and its completion event without implying freshness changed |
| `M17-ENV-01` | check process environment/status/log seam inspected | process lacks provider/MCP secret env; status API has no captures; only Logs output is persisted and redacted/bounded |
| `M17-GIT-01` | pass/fail/result/repair handling | creates no commit/push/merge/deploy/integration-order effect |
| `M17-SQL-01` | raw SQL update/delete/detach/forge of definition/grant/receipt/result/freshness/evidence/route/repair rows | rejected or canonical read fails |
| `M17-SQL-02` | worker receives job without matching exact launch authority/event/receipt; committed grant/definition later retires | worker refuses orphan; committed recovery remains valid after later definition/grant retirement |

The fail/repair/pass black-box fixture uses arbitrarily named change and evidence
agents plus exactly one current-packet check-watch grant. It runs without provider,
network, or remote CI. A daemon is stopped after the child launches and before
terminal acknowledgement. Restart must reconcile that exact child and produce one
result or explicit unknown, never a second execution.

## M19 executable acceptance and security matrix

Every M19 row below is required. Model and renderer rows use deterministic
messages and controlled dimensions; transport rows use the real daemon socket;
the public scenario invokes the built binary. Golden snapshots may pin stable
frames, but structured equality and focused assertions remain authoritative so a
cosmetic edit cannot hide a changed claim, cursor, or action.

| ID | Adversarial setup | Required observable result |
| --- | --- | --- |
| `M19-MODEL-01` | empty, normal, disconnected/stale, capped, and large models | deterministic reduction and pure `View` with no I/O or fact mutation |
| `M19-LAYOUT-01` | sizes `60x18`, `79x23`, `80x24`, `119x31`, `120x32`, and large | exact one/two/three-pane mode; below `60x18` is one stable too-small message |
| `M19-NAV-01` | full key map across navigation, records, detail, and modal focus | `Enter` only inspects; `Esc`, quit, cancel, paging, top/bottom, filter, help, action, and refresh are exact |
| `M19-ROUTE-01` | push beyond 16 routes; reorder/remove selected item; resize/reconnect; duplicate task/claim details with cached and in-flight reads; agent churn | depth stays 16; selection follows stable ID or documented nearest survivor; focus is legal; async caches remain route-owned and bounded; Back preserves or reloads the revealed frame and drops abandoned completions |
| `M19-BRIEF-01` | render a briefing through API, CLI, and TUI; inspect one material claim | claims/order/sources/classification/omissions/hash/cursor are deeply equal, not re-derived; on-demand explanation exactly preserves claim/provenance/current-source diagnoses |
| `M19-DRILL-01` | every urgent displayed aggregate | activation yields the exact complete canonical record IDs that produced the count |
| `M19-TEXT-01` | invalid UTF-8, ESC/OSC, C0/C1, bidi controls, wide/combining and long strings | terminal output is valid, bounded, control-free, directionally safe, and layout-stable |
| `M19-A11Y-01` | normal color, `--color never`, and nonempty `NO_COLOR` | identical meaning through textual state/severity/focus labels; monochrome output has no styling escapes |
| `M19-SYNC-01` | bootstrap at captured high-water while newer events arrive | a final event-head fence rejects the mixed batch; event payloads only invalidate; applied cursor advances only after a fenced refresh |
| `M19-SYNC-02` | exactly 1,000 and more than 10,000 events | bounded 1,000-event pages, ten-page yield, one poll in flight, no starvation or skipped canonical refresh |
| `M19-SYNC-03` | duplicate envelope/notification, malformed or nonmonotonic envelope, unknown kind | no duplicate notification; bad input fails closed; unknown kind invalidates all canonical views |
| `M19-SYNC-04` | activity over 200, notifications over 100, list over 600 | exact retention and three-page screen bounds with visible capped/omitted diagnosis |
| `M19-SYNC-05` | slow/failing sections overlap refresh, reconnect, and a newer generation | at most four non-event reads run concurrently; each result is generation-bound; applied waits for every invalidated section |
| `M19-SYNC-06` | an event commits between the first section read and the last during bootstrap and refresh | final bounded head validation detects the advanced high-water; mixed section generations are never marked live or applied |
| `M19-RECON-01` | kill daemon during bootstrap, poll, canonical refresh, and mutation response | connecting/syncing/reconnecting states are honest; stale cache is labeled; mutations disabled; cursor never jumps |
| `M19-RECON-02` | repeated failure and recovery | delays are 250ms, 500ms, 1s, 2s, 4s, capped at 5s; selection survives when ID remains |
| `M19-RECON-03` | restarted daemon high-water behind applied cursor | cache is discarded and full bootstrap runs; no backward compatibility replay path |
| `M19-FATAL-01` | missing workspace or protocol mismatch | stable fatal diagnosis and next action; no stale state presented as live |
| `M19-BOUND-01` | list limit/cursor/page overflow, response over 16 MiB, hanging ordinary read/poll/briefing/action | defaults/maxima 50/200, cursor 256, event page 1,000, four non-event requests; ordinary 5s, poll 2s, briefing/action 15s deadlines fail closed |
| `M19-READ-01` | navigate/filter/inspect/explain/help/resize/refresh/reconnect/attach | daemon event high-water is unchanged and no local authority record is written |
| `M19-READ-02` | start and refresh with `--project` by exact ID and by name while checkout observations differ | project resolution uses a pure bounded list/show read; Git/checkouts are not refreshed and event high-water is unchanged |
| `M19-ACT-01` | open an available mutation then press every key except `Ctrl+Enter`; inspect a stop grace value; enter a whitespace-padded approval note; approval links a supervisor action | modal shows target ID/revision/consequence; displayed stop grace equals the frozen raw request; the approval note is canonical before freeze and byte-identical on replay; approval review first validates and shows the action condition/typed response/reasons/exact scope and action+approval revisions; no call or event until exact confirmation |
| `M19-ACT-02` | confirmed action with first response lost, then replayed | same request/key returns one committed result and appends exactly one event/effect |
| `M19-ACT-03` | stale revision, denied actor, approval-required policy, reconnect, timeout | conflict/denial/approval/degraded/unknown-response diagnoses remain distinct; no optimistic UI fact |
| `M19-AUTH-01` | authority-looking arbitrary Role/Purpose values and renamed labels | no permission, action, sort, urgency, or target changes; only exact canonical policy matters |
| `M19-ATTACH-01` | inspect attach result containing executable/argv/env/opaque handle | UI shows no env/handle; exact argv executes without a shell through `tea.ExecProcess` |
| `M19-ATTACH-02` | resize/events/restart while suspended; attached process exits zero/nonzero | terminal restores, size refreshes, queued invalidations drain, reconnect occurs if needed, dashboard resumes |
| `M19-RACE-01` | concurrent poll, refresh, reconnect, resize, action, and attach messages | reducer/component suites pass `go test -race`; stale generations cannot overwrite current state |
| `M19-PROGRAM-01` | real Bubble Tea program with controlled input/output/window size | keyboard-only smoke reaches records, detail, modal cancellation, and clean quit without an interactive host |
| `M19-PUBLIC-01` | isolated provider-free daemon and `test/scenarios/operator-tui/run.sh` | built dashboard proves parity, read-only navigation, one replay-safe action, attach, kill/reconnect, and no leaked process/socket |

## M20 executable acceptance and security matrix

The absolute Linux limits are part of the result, not a hardware-relative
suggestion. Warm startup must complete within two seconds. Under saturated launch
work, 200 status and message operations have p99 at most one second and maximum
two seconds; controlled-clock lease reconciliation completes within two seconds.
Twenty project briefing reads have p99 at most two seconds and maximum five;
twenty workspace briefing reads have p99 at most five seconds and maximum ten.
Full doctor, backup create, verify, and restore each complete within 60 seconds;
load generation and verification complete within five minutes. Peak RSS is at
most 512 MiB and database plus referenced fixture artifacts at most 1 GiB.
Nearest-rank percentiles are used. Relative benchmark deltas are recorded only as
additional diagnosis.

| ID | Adversarial setup | Required observable result |
| --- | --- | --- |
| `M20-CUR-01` | fresh database; old or same-marker/divergent canonical/control schema | fresh schema equals the compiled baseline/installed canonical-control digest; every such divergent nonempty DB is refused without DDL, while the exact rebuildable FTS shape is diagnosed separately |
| `M20-CUR-02` | compare baseline tables/queues with auditor registry; corrupt each family and inject an unknown event | every object is classified exactly once and every corruption/unknown type is detected by a full scan |
| `M20-CUR-03` | compare fresh, captured, and restored logical state | current schema, canonical/durable logical digest, and event cursor are exact; derived FTS is diagnosed separately |
| `M20-BKP-01` | concurrent WAL writes before and after the online-backup cut | one exact snapshot high-water is captured; post-cut rows are absent; referenced immutable artifacts match that snapshot |
| `M20-BKP-02` | each live run/check/binding/job/wake/open intent/action/approval class | create returns retryable `backup_not_quiescent`, bounded counts/samples, no published bundle, and no event |
| `M20-BKP-03` | kill/cancel/disk-full after DB copy, artifact copy, manifest, publish, and before response | target is absent or fully verifiable; same request/key replays one result and never publishes a partial directory |
| `M20-BKP-04` | truncate/alter/miss/add an entry; bad mode; symlink, hard-link alias, device, FIFO, traversal, manifest mismatch | offline verify rejects the exact unsafe/corrupt class without following or reading outside the bundle root |
| `M20-BKP-05` | remove the source daemon, socket, data directory, and DB | path-based offline verify still succeeds; backup ID is metadata only |
| `M20-RST-01` | source removed; restore into a new target | standalone DB, referenced artifacts, logical digest, and cursor reproduce exactly; target is pending/inert |
| `M20-RST-02` | existing target, merge/overwrite attempt, symlink escape, partial-copy failure | stable refusal and original target bytes unchanged; no `--force` or in-place path exists |
| `M20-RST-03` | try to start pending target while original source remains live | `restore_not_activated` before DB recovery, listener, workers, capabilities, or any runtime/provider call |
| `M20-RST-04` | retire source and activate with confirmation | target gets a distinct node key, empty capabilities/runtime roots, no binding, unchanged domain cursor, and can then start |
| `M20-RST-05` | alter DB/artifacts or add a nonterminal run/check/binding after restore or activation | activation/first startup returns integrity or `restore_unsafe_nonterminal` before mutation/external call |
| `M20-RUN-01` | inspect public run/check records, lists, briefings, events, terminal controls | no opaque handle appears; terminal bindings are absent; attach/prompt/interrupt/stop/wake require a live node-owned binding |
| `M20-RUN-02` | finish direct and bounded Herdr runs, restart, back up, restore, and query logs | redacted 64-KiB-per-stream immutable artifacts preserve captured/omitted/hash facts; untrusted absence is `run_logs_unavailable`, never empty success |
| `M20-RUN-03` | runtime identity becomes unknown and remains potentially live | `lost` retains capacity and blocks backup until owner confirms native retirement; resolution emits one event and leaves task blocked |
| `M20-HLT-01` | full doctor over 100,000 events and every registered table/artifact/queue | complete bounded read-only report, stable codes/counts/20-sample cap, no event, and completion within budget |
| `M20-HLT-02` | baseline/canonical corruption prevents daemon startup | offline repair inspects private copied DB/WAL state, mutates no source byte, and gives stable derived-rebuild or restore-to-new-directory guidance |
| `M20-LOAD-01` | build profile twice and rename authority-looking role/purpose labels | exact 1/10/100/1,000/100,000/80,000 counts and repeatable logical hash; labels change no authority/admission/order |
| `M20-LOAD-02` | run the complete measured workload | startup, latency, duration, RSS, and disk absolute limits pass and the full environment/percentile report is emitted |
| `M20-LOAD-03` | fill one project with 80,000 events and decisions while every quiet project has one urgent decision | each quiet-project decision remains represented according to deterministic round-robin fairness; 128-claim/64-KiB/provenance bounds hold |
| `M20-BP-01` | race manual and supervisor starts across workspace/project/provider/node limits | transactional counts never exceed 8/2/4/4/20 defaults; refusal is retryable, names the limiting dimension, and appends no event |
| `M20-BP-02` | exhaust provider A and starting slots while provider B and control work continue | provider B progresses and status/message/reconciliation meet latency limits; no transaction spans an external call |
| `M20-BP-03` | kill around message wake claim/effect/result | message send never calls wake inline; one durable message remains; wake settles succeeded/failed/failed-unknown without automatic duplicate prompt |
| `M20-DB-01` | hold an SQLite writer through busy timeout while issuing reads/mutations | reads remain responsive; mutation returns retryable `database_busy`; same idempotent request eventually commits once |
| `M20-FLT-01` | daemon kill, runtime loss, stale leases, duplicate delivery, truncated output, unavailable provider | each yields its documented bounded durable or explicit-unknown result without a fresh uncertain launch |
| `M20-END-01` | 20 deterministic fault/restart cycles after warm-up | no child/socket/temp leak, FD growth above baseline +3, or goroutine growth above baseline +5; optional two-hour soak is not normal CI |
| `M20-SEC-01` | inspect bundle, restore, reports, and failures for authority/secrets/path escape | no node key/token/provider home/live handle/runtime state/unreferenced file is bundled; modes and redaction hold; docs state hashes are not same-UID authenticity |
| `M20-PKG-01` | build Linux amd64 candidate twice with fixed metadata, extract separately | byte-identical archive/checksum; extracted `version` and `doctor --self` pass; nothing is published/installed and no license is implied |
| `M20-ALL-01` | clean tree complete gate | `scripts/check.sh`, race suite, every prior scenario, and personal-beta scenario pass without paid provider calls or hidden cleanup |

## M21 executable workbench and browser-security matrix

M21's normal acceptance uses the provider-free fixture and a real browser engine;
it must not require Herdr, model credentials, an API key, network access, or a
paid call. Recorded provider boundaries separately prove Codex subscription and
Claude authentication/launch behavior. Installed/live provider and Herdr runs
remain explicit opt-in canaries.

On the fixed `personal-100` profile, an already-running service must return the
authenticated shell and first canonical overview within three seconds. Fifty
warm project switches and agent-detail opens have p95 at most 500 milliseconds
and maximum two seconds. Two hundred committed invalidations become visibly
current at p95 at most one second and maximum two seconds. A local fixture
terminal's 200 input/output round trips have p95 at most 250 milliseconds and
maximum one second. Content-hashed production web assets total at most 5 MiB;
one decoded HTTP response remains within the existing 16-MiB bound; activity,
notification, canonical page, and route/cache retention remain no larger than
their M19 bounds; and rendered entity rows stay bounded/virtualized rather than
growing with all 100,000 events. Measurements report browser engine/version,
asset bytes, DOM node peak, JavaScript heap when supported, and p50/p95/p99/max.

| ID | Adversarial setup | Required observable result |
| --- | --- | --- |
| `M21-SVC-01` | fresh supported Linux user with no state/config/runtime roots | service install creates exact private XDG paths and current store, starts one daemon, and `crewfold open` reaches the workbench; it creates no project/model call |
| `M21-SVC-02` | restart/login, duplicate install/start/open, stale socket/PID, unavailable user-service manager | one idempotent service identity and actionable diagnosis; no second daemon, path adoption, broad kill, or hidden fallback; explicit foreground development mode remains intact |
| `M21-WEB-01` | compare browser, local API, CLI, and TUI reads at one fenced cursor | canonical IDs/revisions/counts/claims/evidence/hash/cursor are deeply equal; browser holds no independent domain projection |
| `M21-WEB-02` | malformed/unknown/oversized HTTP result or SSE event, mixed refresh cut, response/event reordering | strict current schema and semantic binding fail closed; payload only invalidates; a final high-water fence prevents mixed state from becoming current |
| `M21-AUTH-01` | direct URL, missing/expired/replayed bootstrap, stolen session, changed Host/Origin, DNS rebinding, wildcard CORS, CSRF, framed page, remote bind | only one exact loopback origin and owner bootstrap/session succeeds; every other request has a stable denial before canonical read/mutation/runtime contact |
| `M21-AUTH-02` | inspect URLs, history, logs, HTML/JS, storage, cookies, HTTP/SSE/WebSocket frames and failures | no node key, provider credential, API key, MCP capability, runtime binding, attach environment, or owner bootstrap secret is exposed; CSP and no-frame policy are exact |
| `M21-ONB-01` | no workspace; choose one valid committed Git repository and fixture provider | browser creates workspace/project/checkout/provider/runtime/policy setup and lands in the empty workbench without displaying or requiring IDs, revisions, sockets, or scenario files |
| `M21-ONB-02` | missing/unborn/unsafe repository, duplicate project, provider unauthenticated/incompatible, Herdr absent or stopped | exact field-level diagnosis and recovery before canonical setup changes; normal onboarding defaults to live Herdr, while an explicit advanced Direct selection remains available for CI/headless operation |
| `M21-CHAT-01` | ask a factual question about current work | answer cites exact canonical records/cursor, appends only bounded conversation history, and creates no domain event/effect |
| `M21-CHAT-02` | say “organize this objective and start” under sufficient exact policy/budget/capacity | one frozen typed graph creates exact objective/tasks/dependencies/assignments/runs automatically; visible receipts link every entity/revision/event; no redundant confirmation |
| `M21-CHAT-03` | graph mixes allowed local work with one over-budget, authority-changing, destructive, external, or publication operation | allowed dependency-independent operations and the gated cut are declared before execution; gated operation has no effect until exact review; approval executes only its frozen operation |
| `M21-CHAT-04` | ambiguous instruction or manager output with unknown op/field, stale revision, cross-scope ID, cycle, invalid budget, excessive size, or prompt-injected authority claim | at most one bounded clarification or exact refusal; malformed graph creates no effect and role/purpose/prose cannot expand authority |
| `M21-CHAT-05` | kill before interpretation seal, during graph execution, after effect/before receipt response, then replay/reconnect | request and operation idempotency recover one exact interpretation/effect set; committed subset and remaining graph are honest; no model reinterpretation or duplicate event/run |
| `M21-LOOP-01` | an active worker submits progress/blocked/completion reports and durable agent messages while the owner workbench is open | each source event atomically advances one project review cursor; concurrent activity coalesces; the daemon automatically persists a manager-originated cited update, one typed owner decision, or an inert reviewed graph without another owner request |
| `M21-LOOP-02` | a workbench worker becomes blocked, fails, repeats a failure, becomes stale, or exceeds wall time | the applied canonical fact reaches the project executive; no generic resume-before-repair or acknowledgement-only approval is created; only a cited consequential executive choice or exact recovery proposal may enter owner review; a CLI-only project retains the conservative M16 approval path |
| `M21-LOOP-03` | stop after lease, after provider output, after manager turn persistence, and while newer worker activity arrives | exclusive-startup recovery immediately requeues the lease; stable operation/output/turn identities prevent another interpretation or duplicate graph; reviewed and requested event cuts advance monotonically and newer activity receives one subsequent pass |
| `M21-PLAN-01` | edit tasks/dependencies/agents/budgets in a generated plan before launch | validation is deterministic, dependency graph remains acyclic/same-project, revisions/budget deltas are visible, and launch freezes exactly the reviewed graph |
| `M21-PLAN-02` | current project/task/agent/policy changes while plan is open | stale plan cannot launch; exact changed inputs and required refresh are shown; no optimistic client-side merge or compatibility rewrite |
| `M21-PLAN-03` | request changes to one pending executive proposal | the exact proposal becomes rejected/inert history, the note becomes one durable instruction to the same executive, and only a newly validated typed proposal may be accepted; model output is never edited in place |
| `M21-CREW-01` | add a second implementation worker, replay response loss, attempt to disable a worker with retained work, then disable an idle non-final worker | exact agent/profile/grant/binding authority changes once; adding starts no work; retained work and the final-worker boundary fail before authority changes; browser and CLI show the resulting worker set and revision |
| `M21-CODEX-01` | recorded Codex CLI authenticated through ChatGPT subscription with `OPENAI_API_KEY` absent | provider doctor, manager turn, and implementation launch succeed through the CLI adapter; no API key is requested/invented/stored; usage is attributed to provider limits, not fabricated API cost |
| `M21-AGENT-01` | open each active/waiting/blocked/terminal/lost agent and compare API truth | task/run/context/messages/claims/checks/evidence/budget/history and available controls exactly match canonical state; no private reasoning or opaque handle is claimed/displayed |
| `M21-AGENT-02` | large/hostile/binary Git diff, out-of-claim changes, checkout moves or disappears during read | bounded status/diff summary is valid UTF-8, control-safe, scoped and truncation-labeled; drift stays distinct from claim and accepted delivery; source is not persisted in browser/server history |
| `M21-AGENT-03` | a completion enters `review` while its task records `changes_requested`; use the displayed retry, replay it, then use a stale/superseded control | the prior review remains immutable, exact run/task revisions atomically reopen the retained assignment and request one fresh context-bound run, replay returns that run, and stale/superseded attempts create no effect |
| `M21-TERM-01` | current live Herdr run; observe readable events, open the advanced protocol console, move pane, restart daemon/browser, send input, resize, interrupt and stop | readable activity is the default inspection surface; the exact run-bound short grant reconnects to the stable terminal, preserves byte order/size, and controls only that current-node pane; canonical state still comes from Crewfold reports/evidence |
| `M21-TERM-02` | no Herdr, direct run, terminal run completes/lost/foreign-node, expired/replayed/cross-run grant | agent inspector and logs remain usable; live terminal capability is absent or revoked; no fallback guesses a PTY or contacts another run/session |
| `M21-UI-01` | keyboard/screen-reader/reduced-motion/high-contrast use at narrow, desktop and wide layouts | every workflow/control is named, ordered and keyboard reachable; focus survives canonical refresh; meaning does not depend on color/motion; selected agent remains coherent |
| `M21-UI-02` | invalid UTF-8, ESC/OSC, C0/C1, bidi, huge labels/messages/logs and 100,000-event activity | text and structure are bounded and safe; lists virtualize/cap with visible omissions; terminal bytes cannot inject workbench DOM/CSS/script or masquerade as canonical UI |
| `M21-PERF-01` | run frozen personal-100 profile through browser bootstrap, project switch, work graph, crew/activity and agent detail plus fixture terminal | all stated latency/asset/response/retention bounds pass with a complete environment/resource report; daemon M20 limits remain unchanged |
| `M21-REC-01` | daemon/browser/service restart during onboarding, conversation, approval, canonical refresh and terminal stream | visibly stale/disconnected states disable unsafe controls, recover from exact cursor/session/operation state, and preserve selected project/objective/agent where still valid |
| `M21-PUBLIC-01` | built binary and real browser from no data using only service/open then browser actions | repository→instruction→automatic crew→agent inspection→decision→successor→restart→redirect/stop→briefing completes without CLI composition, paid call, required Herdr, leaked process/socket/profile, or hidden fixture setup |
| `M21-PUBLIC-02` | authenticated Codex CLI with `OPENAI_API_KEY` absent, Herdr running, and one fresh real repository | browser instruction→typed proposal→request-changes revision→acceptance→dependency-ordered workers→proactive executive reviews→automatic successor→completion survives daemon restart; routine progress creates no owner decision, and desktop/narrow inspection remains understandable |
| `M21-ALL-01` | clean tree complete gate | frontend format/type/lint/unit/component/browser/security/accessibility suites, Go generated/vet/test/race, M21 public scenario, and every M0–M20 scenario pass; production build performs no network fetch and embeds only pinned assets |

## M22 domain-oriented durable-agent matrix

M22's deterministic gate uses the strict Codex app-server fixture and a real
browser. Its final opt-in acceptance uses the installed Codex subscription with
`OPENAI_API_KEY` absent; it must not substitute the old one-response executive
harness or infer success from fixture output.

| ID | Adversarial setup | Required observable result |
| --- | --- | --- |
| `M22-DOM-01` | owner creates several roots and nested agents with arbitrary names/roles, moves one parent, attempts a cycle/cross-domain parent/workstream | one flat canonical tree renders at any bounded depth; valid changes commit once; cycle and cross-domain attempts have no effect; no name, role, or preferred root confers authority |
| `M22-SES-01` | open two arbitrary agents, exchange several turns, interrupt one, kill/restart provider, Herdr, daemon, and browser | each selected agent resumes its own private Codex thread and bounded readable conversation; agent identity and hierarchy remain; no thread/node/private-reasoning value enters public JSON |
| `M22-MSG-01` | agent A sends an exact typed message to agent B, retries the same provider tool call, targets foreign/retired agents, restarts before delivery read | one immutable same-domain message and one exact tool receipt survive; replay is identical; invalid recipients receive nothing; no transcript broadcast occurs |
| `M22-STAFF-01` | owner grants a lead profiles/classes/descendant/concurrency/budget/expiry, then the lead creates implementer, reviewer, and tester children | each allowed child atomically gains definition, same-domain membership, allocation receipt, and three events; no session/task/run starts merely because the child exists |
| `M22-STAFF-02` | wrong profile/class/domain, stale manager membership, cumulative budget/capacity overflow, unlimited child under finite budget, finite/unlimited allocations under unlimited budget, expired grant, revoke/create race, response replay | requests fail closed with stable staffing codes; zero retains canonical unlimited semantics; the race leaves either one complete authorized child or none; no orphan definition/membership/allocation/event exists |
| `M22-CTX-01` | several checkouts/workstreams plus accepted shared knowledge and messages, then each agent calls `crewfold_get_domain_context` | each receives only bounded canonical domain, hierarchy, relevant resources/workstream/assignment/inbox/grants; provider transcript is never treated as knowledge |
| `M22-UI-01` | select and edit every ownership template, author a custom responsibility, create agents/grants, choose explained staffing classes and finite/unlimited budgets, exercise blocked and successful agent/workstream lifecycle transitions, and switch domain/session/assignment/changes/briefing/verification/staffing at desktop and narrow widths using keyboard navigation | templates only prefill editable intent, custom ownership remains available, task classes do not require memorized slugs, zero is visibly unlimited, retired/closed records remain compact history, text is readable, selection is stable, tabs wrap, exact-effect language is clear, no peer-admin maze or synthetic project narrative appears, and advanced raw terminal remains secondary |
| `M22-REAL-01` | fresh local repository, installed Codex subscription, two owner-created durable agents, one exact staffing grant | browser-only owner flow opens real sessions; one agent reads domain context, messages the other, and creates one authorized child; the tree/session/receipts remain exact after restart |
| `M22-AGENT-01` | pre-onboarding and existing-domain owner intent | a separate read-only ephemeral Codex thread returns one closed name/role/charter/policy draft, creates no domain record or event, and requires owner review before creation |
| `M22-AGENT-02` | durable delegation-first agent with an exact staffing grant | the real session receives the owner-reviewed charter and fixed authority boundary, delegates a suitable continuing responsibility through the typed child tool, and cannot exceed its grant |
| `M22-GROUP-01` | owner creates an empty workstream, repeats its title, and places new and existing roots/children | the first creation has explicit visible success and appears in the hierarchy while empty; the client refuses the repeated title; new and existing agent placement is derived from canonical Objective membership; hierarchy remains acyclic; and `default` changes navigation only—not rank or authority |
| `M22-ALL-01` | clean current tree complete gate | generated DB, protocol/local API/store/daemon/web normal+race gates, browser desktop/narrow scenario, all prior offline scenarios, and the explicit real-provider canary pass; removed M21 `/intent` and `/conversation` routes remain unavailable |

## Test suite commands

The complete implemented offline gate is:

```sh
./scripts/check.sh
```

It runs formatting, vet, all package tests, the race suite when supported, and
built-binary scenarios across local API, direct, Herdr, recorded Codex, and
recorded Claude boundaries. The direct messaging
scenario uses only public CLI/MCP surfaces, stops and restarts the daemon after an
offline send, compares inbox JSON byte-for-byte across restart, then has agents in
adjacent standalone clones read, acknowledge, reply, and complete. It also proves
forbidden recipients, oversized bodies, idempotency, bounded packet summary, and
visible wake failure without message loss.
The Herdr variant repeats the same two-agent flow using isolated recorded Herdr
surfaces, proves a successful prompt wake and native attach, and rejects an
incompatible installed schema before launch.

The M20 scale command below is current. The aggregate acceptance/fault/live
spellings remain conceptual tiers until their public dispatch is implemented:

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
