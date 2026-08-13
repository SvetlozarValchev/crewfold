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
gated live conformance call is pending. It supports text and JSON output. Teams,
broader/model-assisted knowledge curation, policy/approval commands, management
briefings, and the TUI remain future contracts.

## Goals

The CLI is both a human interface and a scriptable client. Commands should provide
readable output by default and stable structured output with `--output json`.
Mutations return the durable entity and event cursor they created.

The daemon, workspace, source, agent/task/run, context, message, claim, meeting,
and canonical-knowledge examples below are implemented. Policy and management
sections define intended behavior rather than an available interface.

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
crewfold workspace init personal --socket /path/to/crewfold.sock \
  --idempotency-key initialize-personal
crewfold workspace show personal --socket /path/to/crewfold.sock
crewfold events list --socket /path/to/crewfold.sock --after 0
# Planned:
crewfold watch
```

The current interface retains explicit `--data-dir`/`--socket` paths. Background
start, default path discovery, and watching are later capabilities. If
`workspace init` omits an
idempotency key, the client generates a unique one; callers that may retry should
supply a stable key.

`doctor` checks this binary, the daemon database, or Herdr's installed API schema
and selected live session. `doctor --runtime herdr` does not launch an agent or
create a workspace. It reports the binary version, schema/protocol compatibility,
and session reachability; an unsupported schema is a hard launch gate with upgrade
guidance. `doctor --provider codex` makes no model call. It verifies the binary,
the stable headless JSON/MCP flags Crewfold needs, and existing Codex
authentication. `--codex-binary` and `--codex-home` allow an explicit installation
or auth/config root; the same values can be passed to `daemon run`. Codex child
commands remain in the `workspace-write` filesystem sandbox. Their network is
disabled by default; an operator who has authorized it can pass
`--codex-tool-network-access true` to `daemon run`. This does not select Codex
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
capacity stays reserved. Arbitrary executable/path selection, direct-runtime
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
IDs in caller order. Context packet v3 includes a complete snapshot only when the
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
becoming stale/superseded, clears that record's effect. Existing packet-v3 bytes
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

The fixed packet budget is 32 KiB with a 12 KiB whole-knowledge sub-budget.
`context show --output json` preserves the exact ordered request list and embedded
snapshots; `context explain --output json` shows included and excluded revisions
plus total and knowledge byte accounting. Eligibility is frozen at build, so later
governance never rewrites an existing packet. There is no transcript ingestion,
implicit project retrieval or context delta. Search remains a separate explicit
M15 query; curator processing never inserts search results into a packet.
To give a run explicit knowledge, build this packet first and pass its ID to
`run start --context`; an atomically generated default run packet has no
caller-supplied knowledge links.

## Outcomes and management briefings

```sh
crewfold outcome propose --task TASK_A outcome.yaml
crewfold outcome accept OUTCOME_REVISION
crewfold checkpoint create --project world-engine
crewfold briefing show --project world-engine --since CHECKPOINT_ID
crewfold briefing explain BRIEFING_CLAIM_ID
```

An outcome assessment has a review state separate from its conclusion, so an
authorized reviewer can accept that a deliverable is only partial or not achieved.
`briefing show` derives a bounded view of commitments, accepted delivery,
decisions, verification, compatibility/stability effects, risks, unknowns, and
owner actions. `briefing explain` follows a material claim to its durable source
records and event cursor. Neither command requires provider transcripts or an
optional model-rendered narrative.

## Policy and approvals

```sh
crewfold approval list --pending
crewfold approval inspect APPROVAL_ID
crewfold approval allow APPROVAL_ID
crewfold approval deny APPROVAL_ID --reason "Do not push this branch yet"
crewfold policy explain --actor engine-impl --action git.push --project world-engine
```

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
