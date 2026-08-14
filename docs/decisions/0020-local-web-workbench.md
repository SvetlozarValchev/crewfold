# ADR-0020: Local web workbench as the primary owner interface

- Status: accepted
- Date: 2026-08-14

## Context

Crewfold M20 proves a durable, bounded, recoverable local control plane. The Go
daemon, SQLite store, local API, runtime drivers, provider adapters, manager and
supervisor, checks, outcomes, and management briefings can execute and explain a
complete multi-agent work loop. M19 adds a trustworthy terminal dashboard for
reading that state and performing a closed set of interventions.

The implemented human workflow is nevertheless fragmented. A new owner must
choose a data directory and socket, start a foreground daemon, compose setup and
mutation commands, carry entity IDs and expected revisions, provide launch
scenario files, and only then open the dashboard. The TUI can inspect and control
existing runs, but it cannot create the workspace, register the repository,
describe an objective, review a proposed plan, assign work, or launch the initial
crew. M19's fixtures began from prepared state and therefore did not exercise its
stated full-personal-scenario exit condition.

The product vision is one console in which the local owner can state intent,
organize and launch work, inspect any agent, understand delivery, and intervene.
The existing `web/` directory deliberately deferred that console while the
control-plane model was unproven. M20 has now supplied that proof. Moving directly
to public packaging would make a scriptable backend publicly installable without
first making the intended personal product usable.

## Decision

### One local service and one primary workbench

M21 builds one owner-local web workbench served by the existing Go daemon. The
browser is the primary human interface. It is a client of canonical daemon APIs,
not a second state store or authority. The daemon continues to own SQLite,
authorization, idempotency, scheduling, runtime/provider orchestration, Git
observation, canonical events, and every durable effect.

The initial supported deployment is a Linux user service. Normal installation
uses fixed owner-local defaults derived from XDG directories:

```text
state:   ${XDG_STATE_HOME:-$HOME/.local/state}/crewfold
config:  ${XDG_CONFIG_HOME:-$HOME/.config}/crewfold
runtime: ${XDG_RUNTIME_DIR}/crewfold when set, otherwise <state>/runtime
```

The installer creates private directories and a user-service definition. The
foreground `crewfold daemon run --data-dir --socket` path remains for development,
fault diagnosis, and isolated tests. `crewfold service install|start|stop|status`
and `crewfold open` are the explicit administrative entry points; ordinary
workbench use exposes no data-directory, socket, entity-ID, expected-revision, or
scenario-file ceremony.

M21 does not import or migrate an older database. The repository remains
greenfield with one current baseline. Service setup creates a fresh exact-current
store or opens the exact current store only.

### Embedded TypeScript web application

The frontend is React with TypeScript and Vite. Node and pnpm are
pinned build-time tools only. Production assets are content-hashed, embedded in
the Go binary, and served by the daemon; installation starts no Node process and
adds no Electron or separate frontend service.

The daemon exposes three loopback-only browser transports:

- bounded HTTP JSON requests for canonical queries and typed commands;
- Server-Sent Events for cursor-bearing invalidation and connection state; and
- a separately authorized WebSocket only for interactive terminal byte streams.

Browser handlers reuse the current domain methods, schema validation, scope
binding, expected revisions, idempotency, stable errors, and result validation.
There is no browser-only mutation implementation and no direct browser access to
SQLite, Git, a provider home, a capability file, the MCP socket, or a runtime
handle. SSE events invalidate bounded canonical reads; journal payloads do not
become a client-side source of truth.

The first workbench areas are Workbench, Work graph, Crew, Inbox, Decisions,
Evidence, Activity, Settings, and Health and recovery. The accepted interaction
reference is [`../../web/workbench-mock.html`](../../web/workbench-mock.html).
Visual measurements may evolve, but the mock's product hierarchy is binding:
conversation and objective state occupy the primary surface while one selected
agent remains inspectable alongside them.

### Browser authentication is local but not implicit

Binding to loopback does not by itself stop cross-site requests, DNS rebinding,
or another process owned by the same user. The browser listener binds only exact
loopback addresses and accepts only its generated exact host/port and origin. It
sets no wildcard CORS policy, rejects state changes without an exact CSRF token,
uses `SameSite=Strict` owner sessions, denies framing, serves a restrictive
Content-Security-Policy, and never loads executable assets from a remote origin.

The daemon creates an owner-only web bootstrap secret outside URLs and logs.
`crewfold open` obtains a single-use, short-lived bootstrap grant through the
owner-only local control socket and opens a URL whose fragment is exchanged for a
bounded browser session. The grant is consumed once, is not a bearer for daemon
or MCP APIs, and cannot authorize a terminal stream by itself. A terminal stream
requires a short-lived exact run/session grant minted after a current canonical
run read and policy check. Raw node keys, provider credentials, capability tokens,
runtime bindings, attach environments, and ChatGPT/Codex authentication never
enter browser state or web responses.

M21 has no remote bind, TLS termination, LAN sharing, hosted synchronization,
multi-user identity, organization login, or browser access from another machine.

### Conversation is an execution surface

The workbench conversation accepts owner questions and owner commands. A command
is not merely prose and does not always require a second confirmation. Each turn
is durably classified as `query`, `plan`, or `act` and records its exact workspace
and optional project/objective/task scope.

An `act` turn follows one current path:

```text
owner instruction
  -> bounded manager interpretation
  -> closed typed operation set
  -> structural and semantic validation
  -> current policy/authority/capacity evaluation
  -> execute allowed operations through canonical commands
  -> persist exact receipts and linked entity/event IDs
  -> render committed effects
```

An explicit instruction such as “organize this objective and start” authorizes
the manager invocation and every resulting operation permitted by the current
project policy and frozen budget. Those operations execute without another modal.
The workbench immediately shows what was interpreted, what is executing, what
committed, and what failed. “No confirmation” never means invisible or
unattributed.

An operation pauses before its first effect when it exceeds the frozen monetary,
token, time, scope, or concurrency limit; changes authority or policy; requests
push, deploy, publication, destructive state/filesystem work, or external human
communication; or admits materially different interpretations. The owner receives
an exact review card or one bounded clarification question. Already-allowed
operations are not silently rolled back merely because a separate independent
operation needs approval; the original operation graph must make that partial
ordering explicit before execution.

Conversation prose is presentation, not authority. The accepted operation set,
policy decision, idempotency key, receipts, canonical entity revisions, and event
sequences are authoritative. Malformed, unknown, stale, cyclic, over-budget, or
out-of-scope manager output creates no effect. A lost response replays the exact
same command; it does not ask a model to reinterpret the turn.

The current baseline gains only the records needed to preserve this boundary:
owner conversation, owner turn, frozen interpretation, typed operation graph,
policy result, execution state, approval linkage, and effect receipt linkage.
Canonical objectives, tasks, assignments, runs, decisions, and approvals remain
the existing domain records rather than copies embedded in chat history.

### Codex subscription access is the default OpenAI path

Crewfold launches the installed Codex CLI through the existing provider adapter.
An owner may authenticate that CLI with a ChatGPT subscription. The workbench and
daemon do not require, request, proxy, or store an OpenAI API key in that mode.
Provider diagnosis reports installed/authenticated/usable state without exposing
account identity or credentials. This follows the two local sign-in modes in the
[official Codex authentication documentation](https://learn.chatgpt.com/docs/auth):
ChatGPT subscription access or explicit usage-based API-key access.

Manager turns and implementation runs use the same provider/runtime abstraction.
When Codex is selected, they run through the authenticated Codex CLI and consume
the owner's Codex subscription allowance. API-key authentication remains an
explicit optional provider configuration for owners who choose usage-based API
billing. Parallelism remains subject to Crewfold capacity/budget policy and the
provider's current usage limits.

### Herdr is the normal interactive runtime; drivers remain replaceable

Herdr is the normal workbench runtime because it supplies persistent PTYs, stable
terminal IDs, native attachment, pane output, prompt, interrupt, stop, and
reconciliation. When Herdr is installed, `crewfold service install` manages it as
a companion user service, and browser onboarding and launch retry prove that its
live server is ready before committing setup or launch effects. It is not the
workbench, database, or source of task truth.

Direct/headless execution remains an explicit advanced fallback for CI,
provider-free acceptance, and environments that deliberately do not need an
interactive terminal. It is not the normal browser default. A future tmux,
container, or remote runtime can implement the same capability without changing
the workbench domain model.

The web agent inspector shows canonical task/run/context/message/check/evidence
state for every runtime. Direct runs expose bounded current or archived logs.
When a current live runtime advertises an interactive terminal, the inspector may
open a separately authorized stream.

### Agent inspection shows observation, not private reasoning

For one selected agent/run, the workbench may show:

- assigned objective, task, checkout, context packet, and policy;
- structured progress, blockage, completion proposal, messages, and handoffs;
- claims, bounded Git status/diff summary, drift, checks, and evidence;
- current budget, capacity, elapsed time, durable history, and decisions; and
- bounded direct logs or a live Herdr terminal when available.

The daemon adds bounded read-only Git status/diff endpoints rather than returning
unbounded source or storing a source-code copy. Changed paths are observations;
declared claims and accepted outcomes remain separate facts. Raw terminal bytes
are visibly labeled operational and untrusted, sanitized before non-terminal
rendering, and never complete a task. Crewfold does not expose or claim access to
private model chain-of-thought, unreported provider reasoning, provider-private
session history, or actions invisible to all runtime/MCP/Git/check boundaries.

### CLI and TUI become secondary, not incomplete

The CLI retains full typed command coverage for automation, CI, recovery,
diagnosis, conformance, and advanced administration. Public protocol tests
continue to use it. Documentation no longer presents command composition as the
normal owner workflow.

`crewfold ui` remains a compact keyboard-only operational/SSH fallback for
canonical overview, briefing, activity, inspection, attach, stop/resume, and
approval decisions. M21 does not expand it into a second onboarding or planning
workbench. Herdr remains a runtime terminal host. MCP remains the structured
agent-facing interface. None of these surfaces develops a competing projection
of project truth.

## Consequences

- Crewfold becomes usable through the single owner experience described by the
  product vision before public-release packaging begins.
- The Go daemon remains one deployment and authority boundary even though the
  build gains a pinned frontend toolchain.
- Most existing domain behavior is reused; the largest new durable seam is the
  conversation-to-typed-command and receipt model.
- Browser security, service lifecycle, terminal proxying, and frontend scale are
  new first-class fault boundaries and require executable acceptance tests.
- Herdr becomes the managed interactive default without becoming a second control
  plane; explicit headless work can still select Direct.
- Existing M19 and M20 reviews remain historical evidence. References there to
  “M21 packaging” describe the then-current numbering; public OSS readiness is
  now M22.
- No public upstream, release license, adapter SDK, or release publication occurs
  in M21.

## Rejected alternatives

- Ship M20 as the public product and document the command sequence: this makes
  backend completeness substitute for the intended owner workflow.
- Put the management interface in Herdr/tmux: terminal topology cannot represent
  canonical plans, evidence, policy, decisions, and portfolio understanding.
- Replace the Go daemon with a Node web server: it duplicates authority and makes
  process/database/runtime lifecycle harder to keep exact.
- Run a separate production frontend service or Electron shell: neither is needed
  for a loopback local workbench embedded in one binary.
- Let the browser read SQLite, Git, provider homes, or runtime sockets directly:
  it bypasses current scope, policy, bounds, and audit receipts.
- Treat every chat turn as read-only planning: it preserves the wall of commands
  under a conversational veneer.
- Execute every model-proposed action immediately: owner language does not erase
  scope, budget, external-effect, ambiguity, or authority boundaries.
- Require an OpenAI API key for Codex: the installed Codex CLI already supports
  subscription authentication and is the current provider boundary.
- Remove Direct entirely: CI and deliberately headless operation still need a
  bounded noninteractive runtime, and the runtime interface remains replaceable.
- Display hidden reasoning or infer truth from terminal prose: Crewfold governs
  observable, provenance-linked facts rather than manufacturing introspection.
