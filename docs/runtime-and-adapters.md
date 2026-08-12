# Runtime and adapters

## Two independent adapter axes

Crewfold separates where a process runs from what kind of agent it is.

### Runtime driver

Controls execution and terminal lifecycle:

- create or select an execution surface;
- set working directory and environment;
- launch, observe, attach, interrupt, and stop;
- report runtime handles and lifecycle observations;
- optionally manage tabs, panes, and layout.

Examples: Herdr, direct subprocess, future tmux or remote-node driver.

### Provider adapter

Describes agent-specific behavior:

- launch command and arguments;
- capability and version detection;
- initial prompt delivery;
- lifecycle and blocked-state interpretation;
- native resume/checkpoint handles when supported;
- installation of the Crewfold MCP endpoint or hooks;
- normalized result and usage metadata.

Examples: Codex, Claude Code, OpenCode, generic terminal agent.

Any compatible provider adapter should be usable on any runtime driver that
satisfies its capabilities.

Independence does not imply that every pair is compatible. A runtime must reject a
launch specification it cannot supervise; for example, the in-memory fake runtime
rejects a provider command instead of accepting a run that can never advance.

## Implemented deterministic contract

The first executable contract deliberately keeps the two axes separate:

```text
RuntimeDriver:
  Name
  Launch(operation_id, placement, launch_spec) -> runtime_binding
  Reconcile(operation_id, stored_handle) -> runtime_binding
  Inspect(operation_id, stored_handle) -> runtime_snapshot
  Stop(operation_id, stored_handle, grace_policy) -> stop_result
  Logs(operation_id, stored_handle, tail) -> bounded_logs

Optional interactive capabilities:
  Prompt(operation_id, stored_handle, text)
  Interrupt(operation_id, stored_handle)
  Attach(operation_id, stored_handle, takeover) -> native_attach_spec

ProviderAdapter:
  Name
  Prepare(run, scenario) -> launch_spec
  Bind(run, runtime_binding) -> provider_binding
  Next(run, scenario, runtime_snapshot) -> normalized observation
```

`Launch` is idempotent for a stable operation ID. The fake runtime records one
binding per run ID and returns it on replay. The fake provider validates a bounded
scenario, binds independently of runtime placement, and emits only normalized
progress, blocked, or completion observations. Acceptance is a Crewfold domain
decision over evidence; neither adapter can directly complete a task.

The daemon owns a durable SQLite work queue. It commits run intent and explainable
placement before invoking an adapter, persists an observation cursor after every
accepted report, and can resume a requested, starting, blocked, or checkpointed
active run after restart. Runtime/provider registries are injected by name, so
direct and Herdr runtime support requires no provider-specific branch in the core.

## Implemented direct subprocess runtime

The `direct` runtime launches one detached supervisor per run. The supervisor owns
the child process, process group, capped stdout/stderr files, timeout, stop
fallback, and an atomically replaced state record under the daemon data directory.
The daemon stores an opaque `direct:<run-id>` binding and can construct a fresh
driver after restart to inspect the same supervisor state.

The first direct providers are hidden provider-free worker modes of the Crewfold
binary. `fixture` converts checked-in scenarios into structured process output for
the direct-runtime compatibility gate. `fixture-mcp` reads its immutable briefing
and submits reports/artifacts through authenticated MCP; its messaging fixture can
also list, read, acknowledge, send, wait for, and reply to durable mail. Stdout is
no longer completion or communication authority. Both exercise the same run/task
acceptance decision without model credentials.

Safety boundaries:

- the working directory always comes from the selected checkout; the run command
  accepts no arbitrary executable or working-directory argument;
- only `PATH`, locale, temporary-directory, timezone, the Crewfold run ID, and the
  MCP socket/capability-file paths are inherited; the token itself is not an
  environment value or launch-spec field;
- each output stream has an independent byte cap and omitted-byte counter;
- API log reads heuristically redact secret-like assignments; raw owner-only
  capture files remain local diagnostic evidence and are not shared context;
- completion reports are not accepted until the direct process has settled;
- start, non-zero exit, timeout, graceful stop, forced stop, and unknown identity
  remain distinct outcomes;
- an unknown supervisor/child outcome becomes `lost` and retains task assignment
  and checkout capacity rather than assuming the process stopped.

This is process supervision, not an OS sandbox. The fixed fixture command is
trusted test code; allowing arbitrary project commands later requires explicit
command policy and, where needed, a sandbox/container boundary. The implemented
process identity and process-group behavior is currently Linux-first.

## Implemented Herdr interactive runtime

Herdr already provides the terminal concerns Crewfold should not rebuild:
workspaces, tabs, split panes, persistent sessions, named agents, lifecycle
observations, prompting, output reads, waits, and a local socket/JSON CLI.

Crewfold's Herdr driver:

1. discovers `herdr api schema --json` and currently accepts schema 1/protocol 19;
2. verifies the selected live Herdr session before any launch;
3. creates one isolated, non-focused workspace/root pane per fixture run;
4. launches an argv-preserving Crewfold supervisor into the pane through the
   documented CLI control surface;
5. stores workspace, tab, pane, and stable terminal IDs in an opaque versioned
   handle;
6. resolves the current pane by stable terminal ID after layout moves and daemon
   restart;
7. observes pane presence, foreground process state, and a durable child exit
   record without granting any of those completion authority;
8. implements pane prompt, mailbox wake, `ctrl+c`, native terminal attach, bounded
   read, grace/close stop, and unavailable-session retry classification; and
9. keeps all durable messages, tasks, reports, and future knowledge in Crewfold.

Illustrative mapping:

| Crewfold | Herdr |
| --- | --- |
| Checkout/project view | Workspace |
| Human grouping/layout | Tab |
| Concrete fixture run | Raw pane process with stable terminal identity |
| Future provider-aware run | Named/detected agent in a pane |
| Non-agent watcher/test | Raw pane process |
| Runtime observation | Agent/pane status and events |
| Attach action | Agent or terminal attach |

The mapping is configuration, not identity. Moving a pane does not move a task to
a new project, and a stopped pane does not delete a durable agent definition.

## Generic terminal provider

The baseline provider adapter needs only:

- an executable command;
- a way to send an initial instruction;
- an optional readiness detector;
- an optional completion/blocked detector;
- the Crewfold MCP endpoint and run identity in its environment or config.

This supports unknown terminal agents with reduced lifecycle fidelity. Agents that
cannot call MCP can still be driven, but must use terminal prompts and structured
handoff files or wrappers; Crewfold reports the degraded capability clearly.

## Enhanced provider adapters

Provider-specific integration is additive. An adapter advertises capabilities such
as:

```text
interactive_terminal
headless_execution
native_resume
lifecycle_hooks
structured_result
mcp_client
usage_reporting
session_export_reference
```

The scheduler checks capabilities required by a task. Core code never branches on
provider names when a capability check suffices.

## Adapter contract

A provider adapter implements conceptual operations:

```text
probe() -> version, capabilities, diagnostics
prepare(run, context_packet, endpoint) -> launch_spec
started(runtime_handle) -> provider_handle
deliver(provider_handle, instruction_ref) -> receipt
observe(provider_handle) -> normalized observations
checkpoint(provider_handle) -> optional resume handle
stop(provider_handle, mode) -> result
reconcile(runtime_handle, stored_state) -> current binding
```

A runtime driver implements:

```text
probe() -> version, capabilities, diagnostics
create_surface(operation_id, placement) -> runtime_handle
launch(operation_id, runtime_handle, launch_spec) -> process_handle
send(process_handle, input) -> receipt
observe(process_handle, cursor) -> observations
attach(process_handle, mode)
interrupt(process_handle)
stop(process_handle, grace_policy)
reconcile(stored_handle) -> current state
```

The wider operations above remain the target contract. The implemented interface
now proves launch, binding, inspection, bounded logs, stop, normalization,
acceptance, reconciliation, prompt/delivery, interrupt, attach, and a versioned
Herdr probe. Provider checkpoint, native resume, usage, and richer capability
negotiation arrive with the concrete provider adapters that exercise them.

## Lifecycle authority

Crewfold distinguishes:

- `runtime observation`: pane/process appears working, idle, blocked, or gone;
- `agent report`: agent claims progress, blockage, or completion;
- `domain decision`: Crewfold accepts task state after policy and evidence checks.

No screen scraper can authoritatively complete a task. No agent self-report can
bypass required tests or review. Reconciliation combines the signals and exposes
uncertainty.

## Direct/headless driver

The direct driver launches a child process without a multiplexer. It is required
for:

- deterministic tests and fake providers;
- one-shot headless agent modes;
- CI watchers and local commands;
- environments where Herdr is not installed;
- future service execution.

It captures bounded stdout/stderr, preserves the exit result, supports graceful
and forced stop, and never retains unlimited output in memory. Arbitrary headless
provider commands are not yet exposed.

## MCP coordination surface

Agents use MCP to retrieve briefings and operate on Crewfold records. The endpoint
is scoped to one run identity using a short-lived credential or inherited local
capability. An agent cannot claim to be another run by changing a tool argument.

The MCP server is independent of the runtime driver. A Codex run in Herdr and a
Claude Code headless run see the same coordination semantics.

The implemented subset shares the owner-only local API socket and accepts
JSON-RPC/MCP protocol `2025-06-18`. A node-secret HMAC capability authenticates one
run through MCP `_meta`; SQLite stores only its expiry and context binding. The
server exposes only briefing/context resources and tools for briefing, status,
progress, blockage, bounded text artifacts, completion proposals, and durable
single-recipient mail. Every scope probe is audited. The run worker consumes
normalized queued reports and retains authority over evidence acceptance and final
state.

Mail delivery does not depend on a runtime driver. The database stores the message
and recipient state first, then a separate durable wake job may ask the runtime to
notify an already-live run. The runtime prompt capability is the wake seam. The
direct fixture has no prompt operation, so it records a bounded failure diagnostic
and discovers the queued message by polling. Herdr submits a bounded
inbox-reference prompt (never the message body), records `wake_succeeded`, and
advances queued delivery to delivered; the agent still reads and acknowledges the
durable database message through MCP.

## Adapter testing

Every adapter must provide:

- a fake implementation for domain and scheduler tests;
- capability-probe fixtures for supported versions;
- launch/reconcile/stop idempotency tests;
- failure fixtures for missing binary, bad config, crash, and orphaned process;
- bounded output and redaction tests;
- an opt-in live conformance suite.

Normal unit tests must never launch a paid model session.

## Versioning

Adapters declare the core protocol range and their own schema version. Crewfold
refuses incompatible major versions and explains the mismatch. Unknown capability
fields are ignored; unknown privileged actions are denied.
