# Milestone review — Herdr runtime with fixture agents

## Identity

- Milestone: `M9 — Herdr runtime driver with fixture agent`
- Review status: `passed`
- Implementation commit: `c2ce4b6d9783aaa4a09269469e6f3607916a993d`
- Reviewer: Crewfold local implementation gate
- Date: `2026-08-12`

## Demonstrable outcome

- User-visible capability: Crewfold diagnoses the installed Herdr schema/session,
  starts provider-free fixture agents in isolated Herdr workspaces, observes and
  reconciles them by stable terminal identity, reads pane output, prompts/wakes,
  interrupts, attaches, and stops without making pane state completion authority.
- Public commands: `crewfold doctor --runtime herdr`, `crewfold run start ...
  --runtime herdr --provider fixture-terminal`, `run prompt`, `run interrupt`,
  `run attach`, `run logs`, and the existing durable `run stop`.
- Acceptance scenario: `test/scenarios/herdr-runtime/run.sh`.
- Exact gate: `./scripts/check.sh`.
- Expected result: formatting, vet, all package and race tests, the nine earlier
  built-binary scenarios, and `Herdr runtime acceptance: PASS`, with no model,
  credentials, paid provider, remote, or installed Herdr required.

## Test evidence

| Suite | Result | Evidence |
| --- | --- | --- |
| Herdr protocol client | passed | Recorded compatible/incompatible schema, live snapshot, structured errors, session selection, and stable-terminal moved-pane lookup |
| Runtime component | passed | Launch/reconcile across driver recreation, moved-pane rebinding, prompt, attach, bounded read, interrupt/forced close, missing-pane non-completion, retryable server restart, cross-run handle rejection, and real supervisor exit persistence |
| Daemon/API/CLI | passed | Registered runtime/provider, versioned prompt/interrupt/attach methods, runtime-aware mailbox wake, doctor output, and prior direct-runtime behavior |
| Offline black box | passed | Built binary, stateful recorded Herdr CLI, real pane supervisor/fixture children, two adjacent clones, daemon restart, successful message wake, two-agent request/reply/handoff, completion, and attach |
| Earlier capabilities | passed | All prior scenarios rerun through the complete gate; direct messaging still records its explicit wake failure and polling recovery |
| Live conformance | opt-in | `CREWFOLD_LIVE_HERDR=1 ./test/live/herdr/run.sh` owns a dedicated session and repeats the provider-free two-agent flow; no model provider is involved |

## Compatibility and failure proof

- The installed-schema contract is explicit: Herdr API schema version 1,
  protocol 19, plus the exact request methods the fixture driver uses. This is the
  stable Herdr 0.8.0 protocol contract, not an unversioned text scrape.
- `doctor` rejects schema version 2/protocol 20 and missing required methods with
  an instruction to install a compatible Herdr release or update the driver. The
  black-box scenario runs that probe before starting the daemon or creating any
  Herdr surface.
- Herdr CLI error envelopes retain their upstream code and bounded diagnostic.
  `server_not_running`/connection loss becomes a retryable runtime-unavailable
  condition in the durable worker. A missing stable terminal is different: the
  run becomes lost/failed because pane closure proves neither provider completion
  nor process outcome.
- A cross-workspace pane move changes public workspace/tab/pane IDs but preserves
  the terminal ID. Reconciliation returns a refreshed opaque handle while the
  stored old handle remains resolvable through terminal identity.

## Lifecycle and authority

- The driver creates one non-focused workspace/root pane per run and records the
  surface before dispatching the command. A durable dispatch intent prevents an
  unacknowledged retry from blindly duplicating the external effect.
- A hidden Crewfold pane supervisor launches the fixed fixture command with argv,
  assigned checkout, allowlisted environment, MCP capability references, and
  Herdr's authoritative pane variables. It keeps stdout/stderr on the pane PTY and
  atomically records child start/exit/timeout/interruption.
- Provider/MCP reports remain the only agent completion proposal. Crewfold accepts
  completion only after the supervisor records process exit and scenario evidence
  passes. Neither a shell prompt, empty foreground-process list, pane state, nor
  pane close can complete a task.
- `run stop` sends `ctrl+c`, waits for the configured grace period, closes exactly
  the resolved stable terminal's pane, and verifies its absence. A close whose
  result cannot be proven becomes unknown rather than success.

## Messaging and interactive controls

- The generic optional runtime capabilities are prompt, interrupt, and attach;
  fake/direct drivers do not pretend to implement them.
- Herdr mailbox wake sends only a bounded instruction to inspect Crewfold's
  durable inbox, never the message body. Herdr acknowledgement marks wake success
  and queued delivery as delivered; the agent still reads and acknowledges the
  database record through authenticated MCP.
- `run attach` asks the daemon for a native attach specification, then the CLI
  launches `herdr terminal attach TERMINAL_ID` on the user's stdio. The daemon
  never tries to proxy an interactive terminal through its request socket.

## Security and ownership

- Herdr commands are invoked as argv through the documented CLI; no project text
  or message body is interpreted as a shell command. The one shell submission is
  constructed from absolute Crewfold-owned executable/spec paths using POSIX
  single-quote escaping.
- The runtime command remains a fixed trusted fixture. Arbitrary project commands
  and real model binaries wait for provider-specific policy/capability work.
- Surface cleanup targets only the run's resolved pane. The recorded/live tests
  use isolated temporary state, and the live test uses a dedicated session name.
- Herdr owns layout and PTYs; Crewfold owns runs, tasks, messages, reports, and
  acceptance. No pane metadata is used as the durable database.

## Known limitations and deferrals

- Real Codex/Claude integration and Herdr's `agent.*` lifecycle are deliberately
  absent. The fixture is an ordinary pane process, which keeps provider behavior
  out of this runtime proof.
- Compatibility is intentionally strict at protocol 19. A future Herdr release
  must be added through recorded fixtures and live conformance rather than an
  optimistic version range.
- The driver currently uses Herdr's CLI wrappers for short-lived orchestration.
  A direct socket/event subscriber may later reduce polling, but it must preserve
  the same stable handle and lifecycle authority boundaries.
- The supervisor persists state in Crewfold's data directory, while pane output
  remains Herdr scrollback rather than a second durable capture. `run logs` is
  bounded by the requested terminal rows and applies API redaction.
- Attach requires a real terminal for useful interaction. The offline fixture
  proves command delegation; the live test omits interactive attach so it can run
  unattended.

## Repository hygiene

- No milestone code appears in executable artifact paths, variables, fixture
  identities, environment names, or test names.
- No upstream repository or Git remote was created.
- Default tests remain offline and credential-free.
- `sparq-agent-os` was not touched.

## Decision

- Exit gate satisfied: `yes`.
- Waivers: none.
- Next milestone entry criteria met: `yes`.
- Next milestone: `M10 — Codex provider canary`.
