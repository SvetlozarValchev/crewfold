# CLI experience

## Implementation status

The current binary implements `help`, `version`, self/database diagnostics,
foreground daemon lifecycle, process and workspace status, workspace/event
queries, project/checkout registration and observation, durable
agent/objective/task coordination, immutable context packets, deterministic fake
execution, supervised direct and Herdr fixture subprocesses, run-scoped MCP
reporting, and durable one-recipient agent mail. It supports text and JSON output.
Teams, claims,
meetings, canonical knowledge, policy, and approval
commands in later sections are intended future contracts and are not yet available.

## Goals

The CLI is both a human interface and a scriptable client. Commands should provide
readable output by default and stable structured output with `--output json`.
Mutations return the durable entity and event cursor they created.

The daemon, workspace, source, agent/task/run, context, and message examples below
are implemented. Claims, meetings, knowledge, policy, and management sections
define intended behavior rather than an available interface.

## Daemon and workspace

```sh
crewfold daemon run --data-dir /path/to/state --socket /path/to/crewfold.sock
crewfold daemon stop --socket /path/to/crewfold.sock
crewfold status --socket /path/to/crewfold.sock
crewfold doctor --database --socket /path/to/crewfold.sock
crewfold doctor --runtime herdr
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
guidance.

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
capacity stays reserved. Arbitrary executable/path selection, attach, interrupt,
dry-run, and real model providers remain deferred.

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

## Claims and overlaps

```sh
crewfold claim add TASK_A --write 'src/physics/contact/**' --lease 2h
crewfold claim list --active
crewfold overlap list
crewfold overlap inspect OVERLAP_ID
crewfold overlap resolve OVERLAP_ID --strategy sequence --first TASK_A
```

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

## Meetings

```sh
crewfold meeting create \
  --agenda "Choose ownership of the shared contact schema" \
  --participant engine-impl \
  --participant engine-review \
  --facilitator workspace-manager \
  --from-overlap OVERLAP_ID
crewfold meeting run MEETING_ID
crewfold meeting inspect MEETING_ID
crewfold meeting conclude MEETING_ID --resolution-file resolution.md
```

The first implementation may combine create and run, but the domain operations
remain distinct so a human can inspect the input snapshot.

## Knowledge and context

```sh
crewfold knowledge list --project world-engine --type decision
crewfold knowledge propose --type finding --from-task TASK_A finding.md
crewfold knowledge accept KNOWLEDGE_REVISION
crewfold context build TASK_A --workspace personal --agent engine-impl \
  --expected-task-revision 2 --socket /path/to/crewfold.sock
crewfold context explain CONTEXT_PACKET_ID --workspace personal \
  --socket /path/to/crewfold.sock
```

`context explain` shows why each item was included or excluded and the applied size
budget.

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
