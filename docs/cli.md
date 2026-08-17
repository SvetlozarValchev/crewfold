# CLI experience

## Implementation status

The current binary implements `help`, `version`, self/database/retrieval diagnostics,
foreground daemon lifecycle, process and workspace status, workspace/event
queries, project/checkout registration and observation, durable
agent/objective/task coordination, claims and drift, structured meetings,
canonical decisions/findings, deterministic scoped knowledge search, a rebuildable
knowledge index, a bounded deterministic curator queue and one owner-configured
safe rule, owner-confirmed exact knowledge contradictions, immutable context
packets, deterministic fake
execution, supervised direct and Herdr fixture subprocesses, run-scoped MCP
reporting and knowledge proposal, durable one-recipient agent mail, and an
offline-proven Codex provider adapter. The Claude Code adapter, provider doctor,
and recorded Codex-to-Claude handoff are also implemented; only its separately
gated live conformance call is pending. Owner-granted manager proposals,
immutable launch profiles, deterministic supervisor passes, and exact approval
decisions are also implemented. Owner-created deliverable commitments,
owner-reviewed outcome assessments, immutable checkpoints, and bounded structured
management briefings are implemented. M21 adds private XDG defaults,
`crewfold service install|start|stop|status`, and `crewfold open`, plus the
embedded authenticated workbench for repository/provider onboarding, durable
conversation with a provider-backed project executive, explicit typed-proposal review,
revisioned implementation-crew configuration,
crew/work/inbox/decision/evidence/activity/briefing views, bounded Git and logs,
full health, and a separately authorized live terminal. The Go-native operator dashboard is launched with
`crewfold ui`; ordinary commands continue to support text and JSON output. M20
freezes full health, quiescent backup, source-independent
verify/restore, explicit restore activation, offline repair inspection, and the
provider-free personal-100 load surface documented below. Teams and
broader/model-assisted knowledge curation remain outside the current product
contract.

## Goals

The CLI is both a human interface and a scriptable client. Commands should provide
readable output by default and stable structured output with `--output json`.
Mutations return the durable entity and event cursor they created.

The daemon, workspace, source, agent/task/run, context, message, claim, meeting,
canonical-knowledge, manager, supervisor, approval, outcome, checkpoint, and
briefing examples below are implemented.

## Local service and web workbench

```sh
crewfold service install
crewfold open
crewfold service status
crewfold service stop
crewfold service start
```

`service install` resolves the current owner's XDG state, configuration, and
runtime roots, freezes the invoking owner's bounded absolute `PATH` into both
Crewfold and Herdr user units, writes those private units, reloads the user
manager, and enables and starts Crewfold. Re-run `service install` after changing
an NVM, asdf, or similar toolchain path. The defaults are
`${XDG_STATE_HOME:-$HOME/.local/state}/crewfold` for data and
`${XDG_RUNTIME_DIR}/crewfold/crewfold.sock` when `XDG_RUNTIME_DIR` exists, otherwise
`<state>/runtime/crewfold.sock`. Installation creates no workspace, project,
provider call, model charge, or credential.

The ordinary owner-local install explicitly enables Codex dependency and
documentation network access inside Codex's existing `workspace-write` sandbox.
Use `crewfold service install --codex-tool-network-access false` to opt out. This
service policy does not authorize publishing, deployment, credentials, paid
services, or external side effects; those remain governed by Crewfold's exact
authority and approval paths.

`crewfold open` contacts that private socket, asks the daemon for one short-lived
single-use browser grant, passes the fragment-bearing URL directly to `xdg-open`,
and never prints or logs the grant. The loopback page exchanges it for an
HttpOnly/SameSite-Strict session and opens the primary owner workflow. Normal
repository setup and work orchestration happen in that browser; the CLI remains
the complete typed automation, recovery, conformance, and advanced-administration
surface.

The web Crew page is the ordinary owner path. Automation and diagnosis can use
the same exact authority mutation without manually composing grants and profiles:

```sh
crewfold crew add reviewer --workspace personal --project world-engine \
  --provider codex --runtime herdr --max-concurrency 2 \
  --expected-binding-revision 1 --idempotency-key add-reviewer \
  --socket /path/to/crewfold.sock
crewfold crew disable agent_0123456789abcdef0123456789abcdef \
  --workspace personal --project world-engine --expected-binding-revision 2 \
  --idempotency-key disable-reviewer --socket /path/to/crewfold.sock
```

Adding starts no work. Disabling is rejected while the worker owns accepted or
live work and cannot remove the final implementation worker.

## Operator dashboard

```sh
crewfold ui --socket /path/to/crewfold.sock --workspace personal
crewfold ui --socket /path/to/crewfold.sock --workspace personal \
  --project world-engine --color never
```

The full-screen dashboard is the keyboard/SSH operational fallback. Its exact
screens are Overview, Briefing, Work, Decisions, Checks, Coordination, and
Activity. It reads only the canonical owner-local API, follows its applied event
cursor across reconnects, retains visibly stale state while the daemon is
unavailable, and disables interventions until canonical refresh succeeds.

Navigation, filtering, inspection, explanations, refresh, and attach are
read-only. `Enter` only inspects. An owner mutation starts under `a`, then shows
the exact target ID, expected revision, and consequence; only `Ctrl+Enter` in that
review dialog submits the typed API command. The ordinary CLI exposes the same
mutations for scripts and diagnosis.

Herdr is the terminal/process host rather than a second management surface. An
attach action suspends the dashboard and executes the exact argv returned by the
run API without a shell. Leaving the attached terminal restores and refreshes the
dashboard. The M19 dashboard exposes ordinary attach only; it has no takeover
action.

The dashboard is keyboard-complete. `Tab`/`Shift+Tab` move focus; arrows or
`j`/`k`, `PgUp`/`PgDn`, and `g`/`G` navigate; `/` filters; `Esc` goes back or
cancels; `r` refreshes; `?` opens help; and `q` quits outside a modal. `NO_COLOR`
or `--color never` removes style while retaining textual state, severity, and
focus labels. `crewfold ui` is interactive and rejects `--output`; automation
uses the normal structured CLI/API.

## Daemon and workspace

```sh
crewfold daemon run --data-dir /path/to/state --socket /path/to/crewfold.sock
crewfold daemon stop --socket /path/to/crewfold.sock
crewfold status --socket /path/to/crewfold.sock
crewfold doctor --database --socket /path/to/crewfold.sock
crewfold doctor --retrieval --workspace personal --socket /path/to/crewfold.sock
crewfold doctor --runtime herdr
crewfold doctor --provider codex
crewfold doctor --provider claude
crewfold doctor --full --socket /path/to/crewfold.sock
crewfold workspace init personal --socket /path/to/crewfold.sock \
  --idempotency-key initialize-personal
crewfold workspace show personal --socket /path/to/crewfold.sock
crewfold events list --workspace personal --socket /path/to/crewfold.sock --after 0
```

The foreground interface retains explicit `--data-dir`/`--socket` paths for
development, diagnosis, and isolated testing. Ordinary desktop operation uses the
service defaults above. `daemon run` also accepts an exact
`--web-address 127.0.0.1:<port>`; omission selects an ephemeral IPv4-loopback port
discoverable only through `web.bootstrap`. If `workspace init` omits an
idempotency key, the client generates a unique one; callers that may retry should
supply a stable key.

`doctor` checks this binary, the daemon database, or Herdr's installed API schema
and selected live session. `doctor --runtime herdr` does not launch an agent or
create a workspace. It reports the binary version, schema/protocol compatibility,
and session reachability; an unsupported schema is a hard launch gate with upgrade
guidance. `doctor --provider codex` makes no model call. It verifies the binary,
the stable headless JSON/MCP flags Crewfold needs, a no-effect Linux workspace
sandbox invocation, and existing Codex authentication. A failed sandbox probe is
a hard launch gate: Crewfold reports the local namespace/AppArmor diagnosis before
creating work rather than discovering it inside an assigned task. `--codex-binary`
and `--codex-home` allow an explicit installation
or auth/config root; the same values can be passed to `daemon run`. Codex child
commands remain in the `workspace-write` filesystem sandbox. Foreground daemon
runs keep network disabled by default and require the explicit
`--codex-tool-network-access true` flag; the ordinary installed workbench service
enables dependency and documentation retrieval by default and records that flag
in its exact unit. This does not select Codex
full-access mode. `--codex-sandbox danger-full-access` is available only for an
operator who independently confines the entire Codex process, such as in a
container that mounts only the assigned checkout, and Crewfold additionally
requires `--codex-external-sandbox true`. It must never be used as a workaround
on an otherwise unrestricted host.

`doctor --provider claude` also makes no model call. It checks the installed
Claude Code version, the headless streaming/MCP/permission flags Crewfold relies
on, and authentication status without reporting account or organization identity.
`--claude-binary` and `--claude-config-dir` select an installation and auth/config
root. Daemon runs default to a `1.00` USD per-run ceiling, configurable with
`--claude-max-budget-usd`. Claude runs use one-shot stream JSON, disable session
persistence, ignore normal user/project/local settings sources, require only the
inline Crewfold MCP server, and run in `dontAsk` mode with a bounded tool allowlist.
The native sandbox is enabled and fails closed by default. Only an independently
confined process may set `--claude-external-sandbox true`, which disables the
nested native sandbox; this flag is an assertion about an existing boundary, not
a way to create one.

`doctor --full` asks the running daemon to scan the exact current baseline,
SQLite/FK state, every registered canonical/durable row, known events,
projection/receipt parity, referenced artifacts, derived retrieval, live
bindings, queues, filesystem/restore state, and resource headroom. It appends no
event. JSON reports exact checked/issue counts and at most 20 redacted samples per
fixed check; text and JSON exit nonzero for degraded or failed status.

## Personal-scale recovery and maintenance

```sh
crewfold backup create --socket /path/to/crewfold.sock \
  --to /new/private/backup-directory \
  --idempotency-key nightly-personal-2026-08-14
crewfold backup verify /new/private/backup-directory
crewfold backup restore /new/private/backup-directory \
  --to /new/private/restored-crewfold
crewfold backup activate /new/private/restored-crewfold \
  --confirm-source-retired
crewfold repair inspect /path/to/unstartable-crewfold
crewfold test load --profile personal-100
```

`backup create` is the only online maintenance command. It resolves `--to` once,
sends its canonical absolute nonexistent target and idempotency key to the daemon,
and captures a quiescent SQLite online-backup cut plus exactly referenced check
and immutable terminal-log artifacts. If the key is omitted, the client creates
one; a caller retrying after an unknown response should supply a stable key.
The target must be outside the source data directory. Restore targets must be
outside their source bundle. A publication target cannot use the recovery parent
lock name, and no externally selected source, bundle, repair/activation data
directory, or publication target may be at or below a recovery-reserved staging
component. These overlap/name refusals occur before receipts, staging, or
source/target-parent mutation, while component-sibling prefixes remain valid.
Every recovery CLI path is valid UTF-8, resolves to a non-root canonical absolute
path of at most 4,096 bytes, and rejects C0/C1 terminal controls, Unicode line
separators, and bidirectional formatting controls before filesystem inspection.

`backup verify` and `backup restore` take a bundle directory path, not a backup
ID. `backup_<32-lower-hex>` is result/manifest metadata only; there is no implicit
backup root, registry, alias, search, or source-daemon lookup. Verify and restore
remain usable after the original daemon, socket, data directory, and DB disappear.
Both reject path traversal, symlinks, non-regular/aliased files, unsafe modes,
missing/extra content, hashes, unknown manifest/baseline, and canonical mismatch.

Restore accepts only a nonexistent `--to` directory. It never overwrites, merges,
restores in place, or offers `--force`. The result is deliberately pending: it
contains the exact database/artifacts but no node key, capability, or runtime
state and cannot start a daemon. After stopping/retiring the source installation,
the owner runs `backup activate --confirm-source-retired`. Activation rechecks
full integrity/quiescence, generates a new node key and empty operational roots,
and leaves all domain rows and the event cursor unchanged. The confirmation is an
explicit disaster-recovery assertion and does not require a reachable source.

The bundle excludes keys, tokens, live handles, runtime/check-runtime/Herdr
state, provider homes/credentials, repositories, WAL/SHM, and orphan files. It is
still sensitive because its exact DB contains messages, evidence, and checkout
paths. Manifest and file SHA-256 values detect corruption and inconsistent copy;
they are not signatures and do not authenticate against a malicious same-UID
rewriter.

`repair inspect` is offline and read-only. It refuses a data directory held by a
live daemon, inspects a private copy of its DB/WAL bytes, and emits bounded stable
findings even when that DB prevents startup. Guidance is limited to retry,
derived-index rebuild, lost-runtime retirement, freeing space, verified backup
restore into a new directory, or reporting a defect. It never edits, migrates,
salvages, vacuums, reindexes, deletes an orphan, or repairs canonical state.

Machine results use these exact schemas:

- `urn:crewfold:schema:local-api:full-doctor-result:v1` for online full doctor;
- `urn:crewfold:schema:local-api:backup-create-result:v1` for creation;
- `urn:crewfold:schema:cli:backup-verify-response:v1` for offline verification;
- `urn:crewfold:schema:cli:backup-restore-response:v1` for restoration;
- `urn:crewfold:schema:cli:backup-activate-response:v1` for activation;
- `urn:crewfold:schema:cli:repair-inspect-response:v1` for repair guidance; and
- `urn:crewfold:schema:cli:personal-load-report:v1` for personal load.

Verify reports `status: ok|failed`, exact backup/baseline/cursor/digest/entry/byte
counts, and bounded checks. Restore reports the source backup ID/manifest hash,
canonical target path, exact digest/cursor, and `pending_activation: true`.
Activation reports the same backup ID/target/cursor plus the new node fingerprint
and `status: activated`. Repair reports `ok|guidance_required|uninspectable`, an
available baseline observation, aggregate `artifact_status`, and at most 20 flat
stable findings. The internal inspection report independently retains at most 20
artifact issues and 20 orphan warnings; those path diagnostics are not fields in
the public CLI schema. Each public finding carries one closed remediation value;
no unbounded row or sample-ID list is emitted. Reports are capped at 1 MiB.

`personal-100` creates only an owned temporary directory and accepts no socket,
data-directory, checkout, provider-home, or credential argument. It makes no
network/model/provider call. The exact profile is one workspace, ten projects,
100 arbitrary-role agents, 1,000 tasks, 100,000 known events, 80,000 in one noisy
project, and a bounded eight-unresolved/two-starting phase. Its JSON records the
environment, exact counts, p50/p95/p99/max timings, peak RSS/DB/artifact bytes,
and pass/fail assertions against the fixed M20 budgets.

## Projects and checkouts

```sh
crewfold project add world-engine --repo ~/depot/dev/world-engine \
  --workspace personal --socket /path/to/crewfold.sock
crewfold checkout add world-engine ~/depot/dev/world-engine-2 --mode exclusive \
  --workspace personal --socket /path/to/crewfold.sock
crewfold checkout list world-engine --workspace personal --socket /path/to/crewfold.sock
crewfold project inspect world-engine --workspace personal --socket /path/to/crewfold.sock
```

Registration is read-only. “Checkout” means any concrete Git repository directory:
an adjacent standalone clone/copy such as `world-engine-2` is as valid as a linked
Git worktree. Crewfold groups checkouts by an observed Git-history fingerprint,
not by directory name, parent directory, `.git` location, or shared worktree
metadata. A future separate command would create a Git worktree:

```sh
crewfold checkout create world-engine feature-a --branch crewfold/feature-a
```

## Agent definitions

```sh
crewfold agent create engine-impl \
  --workspace personal \
  --role implementer \
  --provider codex \
  --runtime herdr \
  --max-concurrency 1 \
  --socket /path/to/crewfold.sock
crewfold agent update engine-impl --workspace personal \
  --expected-revision 1 --enabled false --socket /path/to/crewfold.sock
crewfold agent show engine-impl --workspace personal --socket /path/to/crewfold.sock
crewfold agent list --workspace personal --socket /path/to/crewfold.sock
```

Agent definitions are provider-neutral configuration and role records. Creating,
updating, or enabling one never launches a provider or runtime process. Team
grouping is planned but not implemented.

## Objectives and tasks

```sh
crewfold objective create "Ship deterministic vehicle contacts" \
  --workspace personal --project world-engine \
  --budget-tokens 100000 --budget-cents 2000 --budget-seconds 14400 \
  --socket /path/to/crewfold.sock
crewfold task create --workspace personal \
  --project world-engine \
  --title "Implement contact cache" \
  --description "tests pass for repeated contact ordering" \
  --priority 200 --socket /path/to/crewfold.sock
crewfold task depend TASK_B --on TASK_A --workspace personal \
  --expected-revision 1 --socket /path/to/crewfold.sock
crewfold task assign TASK_A engine-impl --lease-seconds 3600 \
  --workspace personal --expected-revision 1 --socket /path/to/crewfold.sock
crewfold task start TASK_A --workspace personal \
  --expected-revision 2 --socket /path/to/crewfold.sock
crewfold task block TASK_A --reason "waiting for an API decision" \
  --workspace personal --expected-revision 3 --socket /path/to/crewfold.sock
crewfold task unblock TASK_A --workspace personal \
  --expected-revision 4 --socket /path/to/crewfold.sock
crewfold task list --workspace personal --project world-engine \
  --ready true --socket /path/to/crewfold.sock
crewfold task show TASK_A --workspace personal --socket /path/to/crewfold.sock
crewfold status --workspace personal --socket /path/to/crewfold.sock
```

Every update, dependency, assignment, or state transition requires the revision
the caller observed. A stale writer receives `revision_conflict`. Budget updates
replace the budget atomically, so the CLI requires token, cost, and time values
together. Zero means no limit for that dimension.

Readiness is derived: a task is ready only when its state is `ready`, it has no
active assignment, and every dependency is completed. The list/show result
includes a stable human-readable reason. Assignment leases expire during task or
status queries; the assignment record and expiry event remain durable.

Manual `task assign` is rejected while accepted manager work has an open
scheduling intent. `task cancel` atomically closes a pending/deferred intent. It
can close `run_requested` retry-pending work only when the exact latest
receipt-linked run is definitively `start_failed`; the cancellation records one
`supervisor.intent_cancelled`, and a later supervisor pass cannot retry it.
Reserved requested/starting/active work cannot be split from its assignment by a
task transition.

`task start` remains a manual coordination transition. Normal execution uses
`run start`, which consumes an existing leased assignment and lets the daemon
advance the task as normalized run observations arrive.

## Runs

Before starting a run, an operator may build and inspect the exact immutable base
briefing:

```sh
crewfold context build TASK_A --workspace personal --agent engine-impl \
  --expected-task-revision 2 --socket /path/to/crewfold.sock
crewfold context show CONTEXT_PACKET_ID --workspace personal \
  --socket /path/to/crewfold.sock
crewfold context explain CONTEXT_PACKET_ID --workspace personal \
  --socket /path/to/crewfold.sock
```

The packet fixes the assigned role, task revision, selected checkout revision,
direct dependencies, scoped tools, policy limits, and reporting instructions. Its
explanation lists both included facts and deliberate exclusions. A packet is
single-use: one run can bind it. If `run start` omits `--context`, the daemon builds
and binds the same packet atomically.

```sh
crewfold run start TASK_A --workspace personal --runtime fake --provider fake \
  --scenario ./scenario.json --expected-task-revision 2 \
  --context CONTEXT_PACKET_ID --socket /path/to/crewfold.sock
crewfold run show RUN_ID --workspace personal --socket /path/to/crewfold.sock
crewfold run list --workspace personal --task TASK_A --socket /path/to/crewfold.sock
crewfold run watch RUN_ID --workspace personal --wait-seconds 30 --socket /path/to/crewfold.sock
crewfold run resume RUN_ID --workspace personal --expected-revision 4 \
  --socket /path/to/crewfold.sock
crewfold run logs RUN_ID --workspace personal --tail 50 \
  --socket /path/to/crewfold.sock
crewfold run stop RUN_ID --graceful --grace-millis 500 --workspace personal \
  --expected-revision 4 --socket /path/to/crewfold.sock
crewfold run prompt RUN_ID --text "check your Crewfold inbox" \
  --workspace personal --socket /path/to/crewfold.sock
crewfold run interrupt RUN_ID --workspace personal --socket /path/to/crewfold.sock
crewfold run attach RUN_ID --workspace personal --socket /path/to/crewfold.sock
crewfold run resolve-lost RUN_ID --workspace personal \
  --expected-revision 5 --note 'Native runtime is retired' \
  --confirm-runtime-retired --socket /path/to/crewfold.sock
crewfold task timeline TASK_A --workspace personal --socket /path/to/crewfold.sock
```

`run start` requires a task with an active assignment. The assigned agent must be
enabled and configured for the requested runtime/provider pair. The scheduler
selects an available writable checkout within the task's project, or validates an
explicit `--checkout` ID. It treats adjacent standalone clones and linked
worktrees identically and persists the reasons for its decision before any launch.

The built-in `fake` adapters read a bounded JSON scenario. The daemon
persists intent, starts asynchronously, records normalized progress, pauses on a
block or explicit checkpoint, evaluates completion evidence, and creates a handoff
only when acceptance passes. `run watch` returns when a run is blocked, needs
review, is stopped/lost, completes, or fails.

The implemented `direct` runtime plus `fixture` provider executes the same
scenario through a real child process in the assigned checkout. It inherits only
an explicit environment allowlist, captures bounded stdout/stderr in owner-only
daemon state, redacts secret-like values at the API boundary, and persists enough
supervisor identity and exit state to reconcile across daemon restart. `run logs`
reports captured and omitted byte counts. `run stop --graceful` requests
termination and records whether forced kill was required. If Crewfold cannot trust
the process identity or outcome, the run becomes `lost`, the task is blocked, and
capacity stays reserved. The owner must first retire that process through its
native control surface, then use `run resolve-lost` with the exact revision, note,
and `--confirm-runtime-retired`. Crewfold does not attempt an external stop; it
records one `run.lost_resolved`, clears the node binding, releases capacity, and
leaves the task blocked for an explicit retry/reassignment decision.
In a project with a workbench executive, that recovery is an exact
`reassign_task` proposal against the blocked task revision and an authorized
launch profile after all reserved run and scheduling-intent authority is gone;
`retry_task` remains specific to a definite `start_failed` run.

Opaque runtime/provider handles are internal node-bound live state and do not
appear in run/check records, briefings, or events. Terminalization clears them.
Before a normal terminal transition, Crewfold persists redacted immutable stdout
and stderr capped at 64 KiB per stream. `run logs` reads those artifacts after
restart/restore; when a lost runtime cannot provide trustworthy bytes, it returns
`run_logs_unavailable` rather than empty successful output. Full Herdr transcripts
and session identity are not retained artifacts. Arbitrary executable/path selection, direct-runtime
attach/interrupt, and dry-run remain deferred; the Codex and Claude adapters below
are the allowlisted real provider commands.

The implemented `fixture-mcp` provider uses that same direct runtime but reports
only through authenticated MCP tools. Its stdout contains runtime metadata, not
authoritative progress records. The run capability is bound to one run, expires
after one hour by default, becomes unusable when the run is terminal, and cannot
select another run through tool arguments. This fixture is the provider-neutral
seam for later Codex, Claude, and Herdr adapters; it is not a live model provider.

The implemented `herdr` runtime uses `fixture-terminal`, which is the same
provider-free scoped MCP fixture under an interactive terminal-provider identity.
The runtime creates one isolated Herdr workspace and root pane per run, launches a
small Crewfold pane supervisor, and keeps the child connected to Herdr's PTY. Its
opaque handle records workspace/tab/pane IDs for diagnosis and a stable terminal
ID for identity. Cross-tab or cross-workspace pane moves therefore do not change
the Crewfold task/run, and a missing pane becomes `lost`/failed rather than
completed. `prompt` and mailbox wake submit terminal input, `interrupt` sends
`ctrl+c`, `attach` delegates to `herdr terminal attach`, and durable `run stop`
closes only that run's pane after its grace policy. Provider completion still
requires an MCP proposal, settled process state, and Crewfold acceptance.

The implemented `codex` provider launches stable non-interactive
`codex exec --json` in the selected checkout. Crewfold supplies only inline,
run-scoped configuration: user config is ignored for the run, existing
authentication still comes from `CODEX_HOME`, the sandbox is `workspace-write`,
interactive approvals are disabled, and the Crewfold MCP server is required. The
MCP server command is the current Crewfold binary's hidden STDIO bridge; only the
socket and private capability-file names are forwarded. The token itself is never
an argument, config value, environment value, or terminal record.

The current Codex slice is one-shot and ephemeral. It can be observed or attached
through Herdr while active, and its JSONL output retains a native thread reference
for diagnosis, but Crewfold does not yet persist/resume that native thread or steer
an active turn. Runtime prompt delivery to a headless Codex process is therefore
not a provider-level steering guarantee. Those controls require the later richer
provider session contract; the current OpenAI app-server surface is intentionally
not made a core dependency here.

The implemented `claude` provider follows the same authority boundary with
`claude -p --output-format stream-json`. Its run-scoped inline MCP configuration
passes only socket and capability-file paths to the hidden bridge; the capability
token is never launch data. `--strict-mcp-config`, an empty settings-source list,
disabled slash commands and browser integration, and `--no-session-persistence`
keep the invocation bounded. Terminal success is diagnostic only: without a
Crewfold MCP report, the run cannot complete.

The Claude adapter is deliberately one-shot like the Codex adapter. It does not
yet own native session resume, active-turn steering, or provider usage records.
A recorded acceptance starts work under Codex, stores the continuation in
Crewfold durable mail, and starts a new Claude run from its immutable briefing;
neither provider-private session identifier crosses that handoff.

## Claims and overlaps

```sh
crewfold claim add TASK_A --workspace personal --project world-engine \
  --checkout CHECKOUT_A --write 'src/physics/contact/**' --lease 2h \
  --mode exclusive --policy notify --socket /path/to/crewfold.sock
crewfold claim add TASK_B --workspace personal --project world-engine \
  --component contact-solver --lease 1h --policy pause_scheduling \
  --socket /path/to/crewfold.sock
crewfold claim list --workspace personal --project world-engine --status active \
  --socket /path/to/crewfold.sock
crewfold overlap list --workspace personal --status open \
  --socket /path/to/crewfold.sock
crewfold overlap inspect OVERLAP_ID --workspace personal \
  --socket /path/to/crewfold.sock
crewfold overlap scan --workspace personal --project world-engine \
  --socket /path/to/crewfold.sock
crewfold drift list --workspace personal --status open \
  --socket /path/to/crewfold.sock
```

These owner-facing commands are implemented. A claim uses exactly one of
`--write`, `--component`, or `--operation`; leases accept Go-style durations such
as `30m` and `2h`. Path claims require `--checkout` when a project has more than
one writable checkout. The supported path grammar is deliberately bounded to
repository-relative literals, `*`, `?`, and whole-segment `**`.

Modes are `exclusive`, `shared`, and `advisory`. Conflict policies are `notify`,
`deny_new`, `pause_scheduling`, and `request_resolution`. Policy is deterministic:
`deny_new` commits no new claim, while `pause_scheduling` prevents a new run for
either affected task until the overlap is resolved by claim release or expiry. It
does not terminate a run already in progress.

`overlap scan` performs read-only Git inspection. Drift is an observation that a
task's checkout contains a dirty path outside that task's active path-claim union;
it does not change the claim. A watcher identity change marks an observation gap.
Shared checkout warnings are explicit because claims coordinate intent but do not
provide operating-system or filesystem isolation. Structured meetings provide the
owner-authorized consolidation path; there is no separate `overlap resolve`
command.

## Messages

```sh
crewfold message send engine-review \
  --workspace personal \
  --kind review_request \
  --task TASK_A \
  --body "Review ordering guarantees in the attached diff" \
  --socket /path/to/crewfold.sock
crewfold inbox --workspace personal --agent engine-review \
  --socket /path/to/crewfold.sock
crewfold thread show THREAD_ID --workspace personal \
  --socket /path/to/crewfold.sock
crewfold thread create --workspace personal \
  --subject "plugandrev / engine-sim-offline contract" \
  --participant plug-agent=TASK_PLUG \
  --participant engine-agent=TASK_ENGINE \
  --socket /path/to/crewfold.sock
crewfold thread invite THREAD_ID --workspace personal \
  --agent integration-reviewer --task TASK_REVIEW \
  --expected-participant-revision 1 \
  --socket /path/to/crewfold.sock
crewfold thread participants THREAD_ID --workspace personal \
  --socket /path/to/crewfold.sock
```

These owner-facing commands are implemented. `message send` creates a thread when
`--thread` is absent and accepts optional `--subject`, `--project`, `--task`,
`--reply-to`, and comma-separated `--artifact-ids`. It sends to exactly one enabled
agent: human recipients and broadcasts are denied. The body is limited to 4096
UTF-8 bytes and at most 16 artifacts may be linked. Owner messages cannot attach
run-scoped artifacts through this command.

`inbox` is an inspection query with a limit from 1 through 50; it does not mark a
message delivered, read, or acknowledged. `thread show` returns ordered immutable
messages plus per-message delivery and wake status. Delivery/read/acknowledgement
mutations are authenticated agent operations exposed through MCP rather than
owner impersonation in the CLI.

`thread create` is the explicit owner boundary for cross-project collaboration.
It requires a subject and two through eight `AGENT=TASK` bindings with unique
agents and unique tasks whose active assignments span at least two projects.
`thread invite` adds one exact
binding with optimistic `--expected-participant-revision`; stale revisions change
nothing. `thread participants` is owner inspection. Agents keep using the existing
MCP inbox/send/read/acknowledge tools: a supplied participant `thread_id` permits
cross-project exchange only when the run's agent, task, and project exactly match
the roster. The roster never broadcasts a message and does not create a task
dependency, claim, meeting, or accepted knowledge record.

## Meetings

```sh
crewfold meeting create \
  --workspace personal \
  --from-overlap OVERLAP_ID \
  --participant engine-impl \
  --participant engine-review \
  --facilitator workspace-manager \
  --socket /path/to/crewfold.sock
crewfold meeting run MEETING_ID --fixture positions-and-proposal.json \
  --expected-revision 1 --workspace personal --socket /path/to/crewfold.sock
crewfold meeting inspect MEETING_ID --workspace personal \
  --socket /path/to/crewfold.sock
crewfold meeting accept MEETING_ID --expected-revision 2 \
  --workspace personal --socket /path/to/crewfold.sock
```

These commands are implemented. The meeting's frozen input, independent positions,
proposal, authority policy, and typed actions remain separately inspectable.

## Knowledge and context

```sh
crewfold knowledge propose finding.md --workspace personal --type finding \
  --from-task TASK_A --socket /path/to/crewfold.sock
crewfold knowledge show KNOWLEDGE_REVISION --workspace personal \
  --socket /path/to/crewfold.sock
crewfold knowledge list --workspace personal --project world-engine \
  --type finding --socket /path/to/crewfold.sock
crewfold knowledge search "contact ordering" --workspace personal \
  --project world-engine --task TASK_A --limit 20 \
  --socket /path/to/crewfold.sock
crewfold knowledge index status --workspace personal \
  --socket /path/to/crewfold.sock
crewfold knowledge index rebuild --workspace personal \
  --socket /path/to/crewfold.sock --idempotency-key rebuild-search
crewfold knowledge accept KNOWLEDGE_REVISION --expected-state-revision 1 \
  --workspace personal --socket /path/to/crewfold.sock
crewfold knowledge dispute KNOWLEDGE_REVISION --workspace personal \
  --socket /path/to/crewfold.sock
crewfold knowledge export /private/engine-knowledge \
  --workspace personal --project world-engine \
  --socket /path/to/crewfold.sock
crewfold knowledge import /private/engine-knowledge \
  --workspace personal --project world-engine \
  --expected-content-sha256 SHA256 --create-scope \
  --socket /path/to/crewfold.sock
crewfold curator queue --workspace personal --project world-engine \
  --socket /path/to/crewfold.sock
crewfold curator rule enable accepted-meeting-resolution-copy \
  --workspace personal --expected-revision 1 \
  --socket /path/to/crewfold.sock
crewfold curator process --workspace personal --project world-engine \
  --apply-safe --socket /path/to/crewfold.sock
crewfold contradiction report LEFT_REVISION RIGHT_REVISION \
  --reason 'The accepted routing rules disagree.' --workspace personal \
  --socket /path/to/crewfold.sock
crewfold contradiction list --workspace personal --project world-engine \
  --socket /path/to/crewfold.sock
crewfold contradiction confirm CONTRADICTION --expected-state-revision 1 \
  --workspace personal --socket /path/to/crewfold.sock
crewfold context build NEXT_TASK --workspace personal --agent engine-impl \
  --include KNOWLEDGE_REVISION \
  --expected-task-revision 2 --socket /path/to/crewfold.sock
crewfold context show CONTEXT_PACKET_ID --workspace personal \
  --socket /path/to/crewfold.sock --output json
crewfold context explain CONTEXT_PACKET_ID --workspace personal \
  --socket /path/to/crewfold.sock --output json
crewfold context refresh RUN_ID --workspace personal \
  --idempotency-key refresh-after-decision \
  --socket /path/to/crewfold.sock --output json
crewfold context delta list RUN_ID --workspace personal \
  --after-sequence 0 --limit 20 --socket /path/to/crewfold.sock
crewfold context delta show CONTEXT_DELTA_ID --workspace personal \
  --socket /path/to/crewfold.sock --output json
crewfold context delta explain CONTEXT_DELTA_ID --workspace personal \
  --socket /path/to/crewfold.sock --output json
```

The proposal file is UTF-8 Markdown beginning with one `# ` title and a non-empty
body. M14 implements only `decision` and `finding`. A proposal requires exactly one
primary source: `--from-task`, `--from-meeting`, or
`--from-meeting-proposal`. Meeting sources must be concluded; a meeting-proposal
source must be accepted and its meeting concluded. Repeated `--supporting-task`,
`--supporting-meeting`, and `--supporting-meeting-proposal` options add provenance.
All sources must share a workspace and project.

The primary source derives the knowledge project. Optional `--project` is a
consistency check and must match it. Applicability is project-wide unless
`--task-scope` narrows it; using `--from-task` does not implicitly make a revision
task-only. The owner may accept, reject, or `mark-stale` a revision using the state
revision it inspected. A successor proposal uses `--supersedes`; accepting it
atomically preserves and supersedes the prior current revision. Authenticated runs
may propose task-sourced knowledge through `crewfold_propose_knowledge`, but only
the local owner may govern it.

`--include` is repeatable and accepts at most 16 unique exact knowledge revision
IDs in caller order. The current context packet includes a complete snapshot only when the
requested revision is accepted, current, fresh, and applicable to `NEXT_TASK`.
Proposed, rejected, stale, superseded, out-of-scope, and over-budget revisions are
excluded with reasons. An unknown ID fails the build. A superseded pin is never
silently replaced; its explanation may name the current replacement, which must be
requested explicitly.

`knowledge search` treats its query as one to 16 literal whitespace-separated
terms, not caller-supplied FTS syntax. The trimmed query is at most 256 UTF-8
bytes; the result limit defaults to 20 and is at most 100. Without `--task`, only
project-wide revisions are eligible. With `--task`, exact task-scoped revisions
rank before project-wide revisions, followed by task/dependency provenance,
freshness horizon, confidence, verification, title-weighted BM25, acceptance time,
and exact revision ID. `--type` is an optional hard filter.

Every JSON match contains the complete exact revision and its
`knowledge_search_v1` tuple explanations, plus the search evaluation instant,
canonical cursor, and index generation. Search is read-only candidate discovery:
it neither governs knowledge nor inserts results into context. A missing, corrupt,
inconsistent, or out-of-date derived index returns `retrieval_degraded` instead of
falling back or reporting an empty success. `knowledge index status` and
`doctor --retrieval` expose health; the doctor exits nonzero for degraded
retrieval. `knowledge index rebuild` reconstructs the projection from canonical
records and may generate an idempotency key when omitted. Exact knowledge and
context reads remain available during retrieval degradation.

`curator queue` is an owner-local read projection over proposed canonical
revisions, ordered by proposal time and ID. Its opaque cursor is valid only for
that stable ordering; the default page is 50 and the maximum is 200. Entries are
`manual_review` unless they have an exact intact derivation for the one supported
rule and that rule is enabled; only then are they `safe_auto_accept`. The queue
also returns the effective rule snapshot, including its enabled state and
revision. The queue itself is not a second editable store.

Every workspace starts with `accepted-meeting-resolution-copy` disabled at rule
revision one. Rule changes require the exact observed revision and are idempotent.
`curator process` without `--apply-safe` is derive-only. Supplying the flag is the
explicit opt-in to safe automatic acceptance. It scans at most 100 candidates and
accepts at most ten per pass. Existing safe derivations are evaluated first; exact
safe sources may be derived and accepted in that same opted-in pass while capacity
remains. A disabled pass may derive
the exact proposed decision from an accepted, concluded meeting but cannot accept
it. After the owner enables the rule, a later pass may accept that same revision.
The transform copies the exact bounded agenda and accepted proposal summary,
project-wide, with `medium`/`supported`/`until_superseded` metadata and one exact
primary `meeting_proposal` source. It never truncates and accepts no caller-defined
transform, actor, source, task scope, or predecessor.
An accepted structured source whose exact text exceeds the transform bounds is
reported in the process result with its source identity and stable skip reason; it
is never truncated and creates no revision, derivation, queue entry, or fact.
An accepted proposal summary above 2 KiB uses
`summary_not_exact_safe_copy`.

Agent proposals remain queued even when marked `high` and `verified`. The curator
does not call a model or provider, read transcripts, use search rank as authority,
run in the background, or expose an agent-facing governance tool. Ordinary manual
`knowledge accept` remains the local-owner path; the curator's narrow internal
path records distinct rule, derivation, authority, and event evidence.

`contradiction report` records a canonical pair of different accepted/current
items with intersecting applicability. Revision order is normalized and the same
immutable pair is unique for all time. A report remains proposed and changes no
retrieval state until the owner uses `contradiction confirm` with the exact state
revision. `contradiction show` returns both exact snapshots and a bounded authority
ledger. `contradiction list` requires the project; omitted status means active
proposed/open records, newest first, default 50 and maximum 200 with no cursor.

An open record quarantines each whole exact participant everywhere it would
otherwise apply, without changing accepted/current currency. Search excludes it
before ranking/limit; an otherwise eligible explicit context pin fails the whole
new build with `knowledge_conflict`. `knowledge dispute` derives total incident
open records and the first 200 sorted IDs. Owner dismissal, or a participant
becoming stale/superseded, clears that record's effect. Existing packet bytes
never change.

`knowledge export DIR` writes a new private directory containing exactly
`manifest.json` and `knowledge.md`. Workspace and project are required. The
manifest is compact canonical JSON over the complete project knowledge snapshot:
all items and revisions in every review/currency state, ordered sources, portable
task applicability anchors, and contradictions in every lifecycle state.
Markdown is a deterministic human rendering, not an alternate authority source.
The command never overwrites `DIR`; successful modes are `0700` for the directory
and `0600` for both files. An unchanged snapshot exports byte-identically even
after daemon restart. The JSON result reports bundle/content/rendering digests,
counts, file sizes, and the read snapshot's event high-water separately from the
portable bytes.

`knowledge import DIR` requires the exact manifest workspace/project and a full
`--expected-content-sha256`. Without `--create-scope`, exact workspace, project,
and task anchors must already exist. With it, import may create the exact missing
scope but never creates a repository, checkout, operational task, meeting, agent,
run, or capability. V1 accepts only an empty canonical target project; there is no
merge, remap, overwrite, or partial import. The same exact bundle replays under
the same or a new key without another event and reports `already_present`.
Malformed bytes, a digest/scope collision, a nonempty target, or an unsafe path
fails before any canonical row or import receipt is committed.

Stable failures distinguish an existing export destination
(`knowledge_export_path_exists`), an unsafe path
(`invalid_knowledge_bundle_path`), invalid canonical bundle bytes
(`invalid_knowledge_bundle`), a digest mismatch
(`knowledge_bundle_digest_mismatch`), an exact-scope mismatch
(`knowledge_import_scope_conflict`), and a nonempty/different imported target
(`knowledge_import_conflict`). Reusing one idempotency key with different import
arguments returns the ordinary `idempotency_conflict`; unexpected durable
I/O/database failures remain `storage_failed`.

An export-side `storage_failed` can occur after the complete directory becomes
visible but before its parent-directory entry is confirmed durable. In that
commit-uncertain case, inspect the existing destination; Crewfold will not
overwrite or automatically remove it on retry.

Import is a local-owner attestation of the validated final snapshot. Portable
bundles do not contain or replay the origin event journal, authority checks,
curator proof rows, or command idempotency. See
[ADR-0013](decisions/0013-portable-project-knowledge-snapshots.md).

The fixed packet budget is 32 KiB with a 12 KiB whole-knowledge sub-budget and an
8 KiB whole participant-roster sub-budget. The current packet also freezes the journal
high-water, up to 32 same-project reverse dependents, up to eight authorized
participant-thread snapshots, and the live delivery policy.
`context show --output json` preserves the exact ordered request list and embedded
snapshots; `context explain --output json` shows included and excluded revisions
plus total, knowledge, and collaboration byte accounting. Eligibility is frozen
at build, so later governance never rewrites an existing packet. There is no
transcript ingestion or implicit project retrieval. Search remains a separate
explicit query; curator processing never inserts search results into a packet.
To give a run explicit knowledge, build this packet first and pass its ID to
`run start --context`; an atomically generated default run packet has no
caller-supplied knowledge links.

`context refresh RUN_ID` is the only operation that scans and constructs live
context. It requires the exact workspace/run and an idempotency key; it accepts no
caller cursor. JSON status is `created`, `pending`, `up_to_date`, or
`rebase_required`. A created/pending result includes the immutable delta. A
pending delta is returned unchanged under another key and blocks scanning until
the exact run acknowledges it. An up-to-date result advances Crewfold's inspected
cursor without creating an empty delta or event.

`context delta list` paginates immutable historical objects by run-local sequence;
the default page is 20 and the maximum is 100. `show` returns the exact typed
object. `explain` returns its identity, base, event interval, change kinds, hash,
and size. These owner queries never mark delivery or consumption, and the CLI
intentionally has no delta-acknowledge command.

The run receives an owner-built pending delta through argument-free
`crewfold_get_context_delta` and acknowledges only its exact ID and sequence with
`crewfold_acknowledge_context_delta`. One delta is capped at 16 KiB, the chain at
64 KiB, and one refresh scans at most 1,000 potentially applicable events. An
unsafe or oversized incremental change returns status
`rebase_required` with a stable reason; it is not an error response. Stop or hand
off that run and start a replacement with a new current-packet base. See [Context
packets and live deltas](context.md).

## Manager proposals and launch profiles

Create target scheduling profiles, create and assign the planning task to the
exact planning agent, create the grant, then create its grant-bound planning
profile. Grant creation requires that current assignment and its exact task and
agent revisions:

```sh
crewfold launch-profile create \
  --workspace personal --project world-engine --agent engine-builder \
  --expected-agent-revision 1 --runtime direct --provider fixture-mcp \
  --scenario worker.json --assignment-lease-seconds 900 \
  --capability-ttl-seconds 900 --socket "$SOCKET"

crewfold manager grant create \
  --workspace personal --project world-engine --objective "$OBJECTIVE" \
  --task "$PLANNING_TASK" --agent constellation-cartographer \
  --expected-task-revision 2 --expected-agent-revision 1 \
  --proposal-kinds task_decomposition,assignment,review,escalation \
  --launch-profiles "$TARGET_PROFILE" --claim-kinds component,path \
  --max-open-proposals 4 --max-actions 16 --max-tasks 8 \
  --max-dependencies 16 --max-claim-requirements 8 \
  --token-limit 20000 --cost-cents 500 --time-seconds 3600 \
  --socket "$SOCKET"

crewfold launch-profile create \
  --workspace personal --project world-engine --agent constellation-cartographer \
  --expected-agent-revision 1 --runtime direct --provider fixture-mcp \
  --scenario planner.json --assignment-lease-seconds 900 \
  --capability-ttl-seconds 900 --manager-grant "$GRANT" --socket "$SOCKET"

crewfold manager propose-tasks --workspace personal --objective "$OBJECTIVE" \
  --planning-task "$PLANNING_TASK" --grant "$GRANT" \
  --profile "$PLANNING_PROFILE" --expected-task-revision 2 \
  --expected-grant-revision 1 --expected-profile-revision 1 --socket "$SOCKET"
```

`manager propose-tasks` invokes the exact already-assigned planning task; proposals themselves come
from its current-packet scoped MCP tools. Omitted planning tuple fields resolve only
when exactly one current tuple is compatible. Role and purpose strings are
arbitrary descriptive metadata. Two agents may share `--role constellation
cartographer`; only the exact grant/task/assignment/profile/packet binding gains
proposal tools.

```sh
crewfold manager grant show "$GRANT" --workspace personal --socket "$SOCKET"
crewfold manager grant list --workspace personal --project world-engine --status active --socket "$SOCKET"
crewfold launch-profile show "$TARGET_PROFILE" --workspace personal --socket "$SOCKET"
crewfold launch-profile list --workspace personal --project world-engine --status active --socket "$SOCKET"
crewfold proposal list --workspace personal --objective "$OBJECTIVE" --status pending --socket "$SOCKET"
crewfold proposal inspect "$PROPOSAL" --workspace personal --socket "$SOCKET"
crewfold proposal accept "$PROPOSAL" --workspace personal --expected-revision 1 \
  --decision-note 'Apply this exact bounded plan' --socket "$SOCKET"
crewfold proposal reject "$OTHER_PROPOSAL" --workspace personal --expected-revision 1 \
  --decision-note 'Keep the current graph' --socket "$SOCKET"
```

A submitted proposal is inert. Acceptance revalidates and applies its entire
closed action set atomically; the JSON result returns the decided proposal,
typed effects, and event cursor. Revoking a grant or retiring a profile uses its
exact revision plus `--reason`; neither operation erases audit history.

## Deterministic supervisor and approvals

```sh
crewfold supervisor policy show --workspace personal --socket "$SOCKET"
crewfold supervisor policy update --workspace personal \
  --enabled true --auto-schedule true --auto-retry-limit 1 \
  --retry-cooldown-seconds 30 --max-active-runs 8 --max-starting-runs 2 \
  --default-project-concurrency 4 --default-provider-concurrency 2 \
  --project-concurrency-json '{"proj_id":2}' \
  --provider-concurrency-json '{"fixture-mcp":1}' --socket "$SOCKET"
crewfold supervisor run --workspace personal --limit 100 --socket "$SOCKET"
crewfold supervisor list --workspace personal --condition dependency_ready --socket "$SOCKET"
crewfold supervisor explain --workspace personal --task "$TASK" --socket "$SOCKET"
crewfold supervisor explain --workspace personal --action "$ACTION" --socket "$SOCKET"
```

Only dependency-ready scheduling under an enabled auto-schedule policy is applied
without owner approval; retry is separately bounded to `0..3` and a cooldown.
Lost/reserved work remains counted until run-first reconciliation. Blocked, stale,
failed, repeated-failure, over-budget, stop/resume, and reassignment responses
remain inert behind one exact approval request.

Ready intents are considered by priority descending, readiness time ascending,
then task ID. Readiness time comes from intent creation, real task-ready or
assignment-expired facts, and dependency completion—not ordinary metadata edits.
An unchanged deferral waits 30 seconds. Only a newly classified fact relevant to
its sealed primary failing dimension can wake it early, so old events and a page
of deferred heads cannot starve later eligible work.

Once scheduling commits, its receipt freezes authority for that one launch.
Profile retirement, agent disablement or revision change, or crossing the
assignment lease timestamp blocks future placement but does not invalidate
worker recovery of the already receipted run. Claim/start still verifies the
exact job, run, task, and active assignment link, and any retry must pass current
authority again.
An accepted manager `request_action` is listed as condition
`manager_escalation`; its JSON action freezes `source_proposal_id` and
`source_action_id`. A retry action uses `prior_run_id` for the immutable failed
run and `run_id` for its fresh requested run.

```sh
crewfold approval list --workspace personal --status pending --socket "$SOCKET"
crewfold approval inspect "$APPROVAL" --workspace personal --socket "$SOCKET"
crewfold approval allow "$APPROVAL" --workspace personal --expected-revision 1 \
  --decision-note 'Allow only this frozen action' --socket "$SOCKET"
crewfold approval deny "$APPROVAL" --workspace personal --expected-revision 1 \
  --decision-note 'Do not apply this action' --socket "$SOCKET"
```

The approval decision result includes both the approval and its bound supervisor
action. Expected revisions make a race or second decision fail rather than apply
twice.

## Local checks and check-watch grants

The owner first creates an exact local command allowlist entry and one named task
criterion:

```sh
crewfold check definition create unit \
  --workspace personal \
  --project world-engine \
  --executable /usr/local/bin/go \
  --arg test --arg ./... \
  --working-directory . \
  --timeout 10m \
  --output-byte-limit 65536 \
  --socket "$SOCKET"

crewfold check requirement create \
  --workspace personal \
  --task "$TASK" \
  --criterion unit \
  --statement 'The unit suite passes at a clean repository HEAD' \
  --definition unit \
  --definition-revision 1 \
  --expected-task-revision "$TASK_REVISION" \
  --socket "$SOCKET"
```

The definition is an executable plus fixed ordered argv, never a shell command.
There are no stdin, environment, credential, provider, MCP, agent-role, or
launch-profile-purpose options. The local owner trusts the executable: direct
checks are not a no-network or no-Git sandbox.

The owner may run and inspect it directly:

```sh
crewfold check run unit --task "$TASK" --workspace personal \
  --checkout "$CHECKOUT" --socket "$SOCKET"
crewfold check inspect "$CHECK_RUN" --workspace personal --socket "$SOCKET"
crewfold check logs "$CHECK_RUN" --workspace personal --socket "$SOCKET"
crewfold check watch --workspace personal --project world-engine --socket "$SOCKET"
```

When `--checkout` is omitted, the server selects the task's currently reserved run
checkout, then its latest run checkout in a stable order, or fails closed. `check
run` returns durable asynchronous intent. `inspect` keeps command outcome,
launch/terminal HEAD and dirty observations, current freshness, named criterion
state, bounded artifact metadata, mechanical evidence, notification routing, and
repair state separate. `logs` returns only bounded redacted retained output.

Only a latest exact-definition pass whose launch and terminal observations have
the same clean nonempty HEAD is `verified`. A dirty pass is diagnostic and
`unknown`; a later HEAD change or dirty observation is monotonically `stale`.
Returning to the old HEAD does not revive it.

`check watch` is one bounded reconciliation/freshness/routing pass. It does not
launch every missing check. Missing, running, failed, stale, and unknown criteria
remain visible in `check requirement list` and JSON output.

An agent receives the watcher surface only through an exact owner grant:

```sh
crewfold check grant create \
  --workspace personal \
  --project world-engine \
  --agent "$WATCH_AGENT" \
  --expected-agent-revision "$WATCH_AGENT_REVISION" \
  --definition unit@1 \
  --operation run \
  --operation inspect \
  --operation propose_repair \
  --max-pending 8 \
  --max-in-flight 2 \
  --socket "$SOCKET"

crewfold check grant show "$CHECK_GRANT" --workspace personal --socket "$SOCKET"
crewfold check grant revoke "$CHECK_GRANT" --workspace personal \
  --expected-revision 1 --reason 'Rotate watcher authority' --socket "$SOCKET"
```

The current-packet agent run derives project, actor, checkout resolution,
definitions, and operations from that exact current grant. It cannot also carry
manager authority. `AgentDefinition.Role` and `LaunchProfile.Purpose` never confer check
authority; arbitrary same-role agents without the grant are denied.

Explicit evidence-review and coordination duties are exact routes:

```sh
crewfold check route create \
  --workspace personal \
  --project world-engine \
  --definition unit \
  --trigger nonpass \
  --duty evidence_review \
  --agent "$EVIDENCE_AGENT" \
  --expected-agent-revision "$EVIDENCE_AGENT_REVISION" \
  --socket "$SOCKET"
crewfold check route retire "$ROUTE" --workspace personal \
  --expected-revision 1 --socket "$SOCKET"
```

Every nonpass also routes to the exact current task assignment. With no current
assignment, Crewfold records `unroutable`; it never guesses from role or history.
Delivered inbox messages identify subsystem sender `crewfold-check-worker`, not
the owner or an agent run.

Repair proposals default to disabled:

```sh
crewfold check policy configure \
  --workspace personal \
  --project world-engine \
  --repair-proposals enabled \
  --repair-profile "$REPAIR_PROFILE" \
  --repair-profile-revision "$REPAIR_PROFILE_REVISION" \
  --max-open-repairs 4 \
  --expected-revision 1 \
  --socket "$SOCKET"

crewfold check repair list --workspace personal --project world-engine \
  --status pending --socket "$SOCKET"
crewfold check repair inspect "$REPAIR" --workspace personal --socket "$SOCKET"
crewfold check repair accept "$REPAIR" --workspace personal \
  --expected-revision 1 --decision-note 'Create the bounded repair task' \
  --socket "$SOCKET"
crewfold check repair reject "$REPAIR" --workspace personal \
  --expected-revision 1 --decision-note 'No repair task' --socket "$SOCKET"
```

An agent proposal is inert and cannot choose the repair agent, profile, command,
budget, or task effect. Only owner acceptance can create the one exact-profile
repair task and scheduling intent. A later fresh pass makes a pending proposal
stale.

No check command completes a task, records policy acceptance, commits, pushes,
merges, deploys, or chooses integration order.

## Outcomes and management briefings

```sh
crewfold outcome commitment add release-ready --task TASK_A \
  --title "Release-ready contact cache" \
  --criterion "deterministic contacts pass" \
  --criterion "compatibility effects are recorded" \
  --workspace personal --socket /path/to/crewfold.sock
crewfold outcome commitment show OUTCOME_COMMITMENT_ID \
  --workspace personal --socket /path/to/crewfold.sock
crewfold outcome commitment list --task TASK_A \
  --workspace personal --socket /path/to/crewfold.sock

crewfold outcome propose --task TASK_A outcome.yaml \
  --workspace personal --socket /path/to/crewfold.sock
crewfold outcome show OUTCOME_ASSESSMENT_ID \
  --workspace personal --socket /path/to/crewfold.sock
crewfold outcome list --task TASK_A \
  --workspace personal --socket /path/to/crewfold.sock
crewfold outcome accept OUTCOME_ASSESSMENT_ID --expected-state-revision 1 \
  --workspace personal --socket /path/to/crewfold.sock
crewfold outcome reject OUTCOME_ASSESSMENT_ID --expected-state-revision 1 \
  --workspace personal --socket /path/to/crewfold.sock

crewfold checkpoint create --project world-engine \
  --workspace personal --socket /path/to/crewfold.sock
crewfold checkpoint show CHECKPOINT_ID \
  --workspace personal --socket /path/to/crewfold.sock
crewfold checkpoint list --project world-engine \
  --workspace personal --socket /path/to/crewfold.sock
crewfold briefing show --project world-engine --since CHECKPOINT_ID \
  --workspace personal --socket /path/to/crewfold.sock
crewfold briefing explain BRIEFING_CLAIM_ID --briefing BRIEFING_ID \
  --workspace personal --socket /path/to/crewfold.sock
```

The commitment is an immutable owner promise bound to one exact task and must
exist before its assessment. `outcome propose` accepts one UTF-8 JSON or YAML
document, bounded to 64 KiB, with exactly this wrapper:

```yaml
commitment: outcommit_0123456789abcdef0123456789abcdef
assessment:
  conclusion: partial
  delivered_scope: ["deterministic contact ordering"]
  unmet_scope: ["operator dashboard"]
  decision_revision_ids: []
  evidence: []
  effects: []
  deviations: []
  risks: []
  unknowns: []
  follow_up_task_ids: []
  owner_attention: []
```

The required `--task` is transport authority input and is not duplicated in the
document. The daemon resolves the named commitment and rejects a task mismatch.
All assessment arrays are required, including when empty. Evidence input may
refer only to a `handoff` or `check_requirement_evidence`; Crewfold derives its
class, freshness, strength, and current truth.

An outcome assessment has a review state separate from its conclusion, so the
local owner can accept that a deliverable is partial, not achieved, or unknown.
Accepting a successor atomically supersedes the prior current accepted assessment;
rejecting a proposal leaves the current accepted assessment unchanged.
`briefing show` derives a bounded view of commitments, accepted delivery,
decisions, verification, compatibility/stability effects, risks, unknowns, and
owner actions at the current captured workspace high-water. Its optional exact
checkpoint is an exclusive lower bound; callers cannot select a historical
cursor. `briefing explain` follows a material claim within the exact briefing to
its durable source records and event cursor. Briefing reads emit no event and do
not require provider transcripts.

## Output and scripting rules

- `--output json` emits one JSON result on stdout; diagnostics go to stderr.
- Watch commands will use newline-delimited JSON with resumable cursors under
  `--output json`.
- IDs are accepted wherever names are ambiguous.
- Mutations accept `--idempotency-key`.
- `--yes` only suppresses confirmation for actions already authorized by policy.
- Exit code `0` means the requested operation reached its documented success state;
  accepted asynchronous intent is reported distinctly from completed effect.
- Destructive or external commands support `--dry-run` where meaningful.

### Exit codes

The CLI uses these process exit classes:

| Code | Meaning |
| --- | --- |
| `0` | The requested synchronous operation succeeded |
| `1` | An operational/internal check or requested operation failed |
| `2` | Command-line usage, arguments, or command selection were invalid |

Machine-readable errors use the versioned [error response
schema](../protocol/schemas/cli/v1/error.response.schema.json) and are written to
stderr. Successful JSON responses are written to stdout. No command emits a stack
trace unless a future explicit debug facility documents that behavior.
