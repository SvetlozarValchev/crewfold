# CLI experience

## Implementation status

The current binary implements `help`, `version`, self/database diagnostics,
foreground daemon lifecycle, process and workspace status, workspace/event
queries, project/checkout registration and observation, and durable
agent/objective/task coordination. It supports text and JSON output. Runs, teams,
claims, messages, meetings, knowledge, policy, and approval commands in later
sections are intended future contracts and are not yet available.

## Goals

The CLI is both a human interface and a scriptable client. Commands should provide
readable output by default and stable structured output with `--output json`.
Mutations return the durable entity and event cursor they created.

The daemon/workspace examples below are implemented. Later sections define
intended behavior, not an implemented interface.

## Daemon and workspace

```sh
crewfold daemon run --data-dir /path/to/state --socket /path/to/crewfold.sock
crewfold daemon stop --socket /path/to/crewfold.sock
crewfold status --socket /path/to/crewfold.sock
crewfold doctor --database --socket /path/to/crewfold.sock
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

`doctor` checks this binary or the daemon database. Later capabilities extend it
to Git, runtime drivers, provider adapters, and permissions without launching an
agent.

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

`task start` changes coordination state only. Runtime launch begins in the next
capability and will use a separate `run` command.

## Runs

```sh
crewfold run start --task TASK_A
crewfold run list --active
crewfold run attach RUN_ID
crewfold run interrupt RUN_ID
crewfold run stop RUN_ID --graceful
crewfold run resume --task TASK_A --agent engine-impl
```

`run start` prints the placement decision before launch when interactive. A
`--dry-run` option returns the placement and context packet without creating a run.

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
  --kind review_request \
  --task TASK_A \
  --body "Review ordering guarantees in the attached diff"
crewfold inbox
crewfold message ack MESSAGE_ID
crewfold thread show THREAD_ID
```

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
crewfold context build --task TASK_A --agent engine-impl
crewfold context explain CONTEXT_PACKET_ID
```

`context explain` shows why each item was included or excluded and the applied size
budget.

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
