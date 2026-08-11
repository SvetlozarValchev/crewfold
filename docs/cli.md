# CLI experience

## Goals

The CLI is both a human interface and a scriptable client. Commands should provide
readable tables by default when interactive and stable structured output with
`--json`. Mutations should return the durable entity and event cursor they created.

The examples below define intended behavior, not an implemented interface.

## Daemon and workspace

```sh
crewfold daemon start
crewfold daemon run --foreground
crewfold doctor
crewfold workspace init personal
crewfold status
crewfold watch
```

`doctor` checks the database, socket, Git, runtime drivers, provider adapters, and
permissions without launching an agent.

## Projects and checkouts

```sh
crewfold project add world-engine --repo ~/depot/dev/world-engine
crewfold checkout add world-engine ~/depot/dev/world-engine-2 --mode exclusive
crewfold checkout list world-engine
crewfold project inspect world-engine
```

Registration is read-only. A separate command would create a Git worktree:

```sh
crewfold checkout create world-engine feature-a --branch crewfold/feature-a
```

## Agents and teams

```sh
crewfold team create engine
crewfold agent create engine-impl \
  --role implementer \
  --provider codex \
  --runtime herdr \
  --team engine
crewfold agent create engine-review --role reviewer --provider claude
crewfold agent list
crewfold agent inspect engine-impl
```

Creating an agent definition does not launch it.

## Objectives and tasks

```sh
crewfold objective create "Ship deterministic vehicle contacts"
crewfold task create \
  --project world-engine \
  --title "Implement contact cache" \
  --deliverable "tests pass for repeated contact ordering" \
  --touches 'src/physics/contact/**'
crewfold task depend TASK_B --on TASK_A
crewfold task assign TASK_A engine-impl
crewfold task list --ready
crewfold task inspect TASK_A
```

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

- `--json` emits one JSON result on stdout; diagnostics go to stderr.
- Watch commands use newline-delimited JSON with resumable cursors under `--json`.
- IDs are accepted wherever names are ambiguous.
- Mutations accept `--idempotency-key`.
- `--yes` only suppresses confirmation for actions already authorized by policy.
- Exit code `0` means the requested operation reached its documented success state;
  accepted asynchronous intent is reported distinctly from completed effect.
- Destructive or external commands support `--dry-run` where meaningful.
