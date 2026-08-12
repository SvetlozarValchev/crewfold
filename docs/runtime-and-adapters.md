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

## Implemented deterministic contract

The first executable contract deliberately keeps the two axes separate:

```text
RuntimeDriver:
  Name
  Launch(operation_id, placement, launch_spec) -> runtime_binding
  Reconcile(operation_id, stored_handle) -> runtime_binding

ProviderAdapter:
  Name
  Prepare(run, scenario) -> launch_spec
  Bind(run, runtime_binding) -> provider_binding
  Next(run, scenario) -> normalized observation
```

`Launch` is idempotent for a stable operation ID. The fake runtime records one
binding per run ID and returns it on replay. The fake provider validates a bounded
scenario, binds independently of runtime placement, and emits only normalized
progress, blocked, or completion observations. Acceptance is a Crewfold domain
decision over evidence; neither adapter can directly complete a task.

The daemon owns a durable SQLite work queue. It commits run intent and explainable
placement before invoking an adapter, persists an observation cursor after every
accepted report, and can resume a requested, starting, blocked, or checkpointed
active run after restart. Runtime/provider registries are injected by name, so a
future direct or Herdr runtime does not require provider-specific branches in the
core.

## Herdr as the preferred interactive runtime

Herdr already provides the terminal concerns Crewfold should not rebuild:
workspaces, tabs, split panes, persistent sessions, named agents, lifecycle
observations, prompting, output reads, waits, and a local socket/JSON CLI.

Crewfold's Herdr driver should:

1. discover the installed API schema rather than assuming an unversioned CLI;
2. create or map Crewfold projects to Herdr workspaces;
3. create topology separately from starting an agent;
4. assign stable Crewfold metadata to runtime handles;
5. use structured JSON responses and socket events where possible;
6. reconcile named agents and panes after either daemon restarts;
7. treat Herdr states as observations, not task-completion authority;
8. keep all durable messages, meetings, tasks, and knowledge in Crewfold.

Illustrative mapping:

| Crewfold | Herdr |
| --- | --- |
| Checkout/project view | Workspace |
| Human grouping/layout | Tab |
| Concrete interactive run | Named agent in a pane |
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

The wider operations above are the target contract. The smaller implemented
interface proves launch, binding, normalization, acceptance, and reconciliation;
probe, delivery, attach, interrupt, stop, checkpoint, and usage arrive with the
capabilities that exercise them.

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

It should capture bounded stdout/stderr, preserve the exit result, support graceful
and forced stop, and never retain unlimited output in memory.

## MCP coordination surface

Agents use MCP to retrieve briefings and operate on Crewfold records. The endpoint
is scoped to one run identity using a short-lived credential or inherited local
capability. An agent cannot claim to be another run by changing a tool argument.

The MCP server is independent of the runtime driver. A Codex run in Herdr and a
Claude Code headless run see the same coordination semantics.

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
