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

### The owner talks to one durable project executive

The primary conversation is not a form that asks the owner to choose a model
operation mode. There is no owner-facing `query`/`plan`/`act` switch. The owner
addresses one named project executive in ordinary language. Crewfold persists the
instruction first and then hosts a real provider run for that executive through
the same Herdr, capability, context-packet, and MCP boundary used by other agents.

Workbench onboarding creates one explicit project-direction objective and the
minimum current authority tuple needed to serve it: a persistent executive agent
definition, a durable assigned planning task, an owner-authored manager grant,
the worker launch profiles named by that grant, and one management launch profile
bound to the grant. These records are visible canonical state. The executive is
not inferred from `AgentDefinition.Role`, a prompt label, a provider session, or a
browser-only setting.

Every exchange is short lived and durable:

```text
owner instruction
  -> durable owner turn and frozen canonical event cut
  -> queued executive exchange
  -> exact manager grant + planning assignment + launch profile
  -> Herdr-hosted Codex/Claude run with scoped Crewfold MCP
  -> read-only executive context and ordinary inbox/message tools
  -> typed proposals and one typed owner response
  -> structural, semantic, authority, revision, and budget validation
  -> durable answer/proposal/decision links and exact run receipt
```

The executive agent identity, owner conversation, project knowledge, messages,
proposals, and canonical history persist across exchanges. Provider thread or
terminal continuity may be used as a runtime optimization in a later milestone,
but it is never the record or authority. A new provider process may therefore
answer the next turn without losing Crewfold continuity.

The manager planning task remains assigned after a successful, failed, stopped,
or interrupted executive exchange. Terminalizing an executive run clears its
node-bound runtime binding and archives bounded logs, but it does not complete,
block, revise, or release the long-lived planning task. A new exchange cannot
start while an earlier exchange for that assignment is live. Assignment expiry,
grant revocation, provider failure, capacity, and runtime failure remain visible
canonical blockers rather than reasons to fall back to a hidden interpreter.

The executive receives two additional manager-only MCP capabilities. A frozen
`crewfold_get_executive_context` read returns the exact owner instruction,
conversation history, bounded project state, and citation namespace captured for
that exchange. `crewfold_respond_to_owner` submits exactly one bounded typed
response and terminalizes the exchange after the provider process exits. The
response may classify its result as a read-only answer, material update,
clarification/decision, proposal, or refusal. This classification is rendered
after the response; it is not authority selected by the owner before sending.

The existing manager proposal tools remain the only path for an executive to
suggest task decomposition, assignments, review work, or escalation. A response
may link only proposals created by the same executive run and citations from its
frozen context. Proposal acceptance remains an explicit canonical owner action.
An accepted proposal is validated again against the current grant, revisions,
scope, budget, and policy before its first effect; the deterministic supervisor,
not the executive prose, launches ready work.

Proposal provenance is immutable. “Request changes” never edits model output in
place: it rejects the reviewed revision with the owner's bounded note, records a
new durable instruction in the same executive conversation, and waits for a new
typed proposal. The earlier proposal remains visible history and inert. Only the
new exact revision can later be accepted.

An explicit owner instruction may authorize an already reviewed, closed typed
operation when the current policy permits it. Destructive work, publication,
external communication, credentials, budget/authority changes, and materially
ambiguous choices still stop before their first effect and become an exact owner
decision. “No extra confirmation” never means an untyped or invisible effect.

Conversation prose is presentation, not authority. Typed proposals, decisions,
policy results, idempotency keys, receipts, canonical entity revisions, and event
sequences are authoritative. Malformed, unknown, stale, cyclic, over-budget, or
out-of-scope executive output creates no effect. A lost request or response
replays the same durable exchange; it does not ask another model to reinterpret
the owner instruction.

A rendered question is not justified merely because the executive emitted valid
choices. A consequential owner decision exists only when two or more valid owner
choices would materially change authorized project state and current policy,
accepted dependencies, and deterministic scheduling do not already select the
next step. Routine progress, dependency release, ordinary scheduling, completion,
failure acknowledgement, and an unresolved blocker are not owner decisions. The
executive must explain those facts, wait for more evidence, or submit an exact
recovery/replanning proposal instead of manufacturing a choice.

The previous one-shot `CodexOwnerInterpreter` path is not a second steady-state
manager. It is removed from the current workbench contract rather than retained
as a fallback. Provider-free fixtures may implement the same executive MCP
contract for tests, but production owner conversation always traverses a visible
agent definition, grant, run, runtime binding, and response receipt.

### Worker activity returns to the same executive

Owner HTTP turns are not the only time the planning manager runs. Once a project
has an open owner conversation, application of a worker-originated structured
progress, blocked, or completion report and creation of a worker-originated
durable message advance one coalesced `owner_manager_review_jobs` cursor in the
same database transaction as the resulting canonical state. Merely receiving a
pending report is not enough: the executive must see the applied run/task
projection at the frozen cut. CLI-only projects with no owner conversation do
not silently consume a provider turn.

One bounded daemon worker leases that cursor and creates a review exchange for
the same durable project executive. It does not invoke a separate interpreter.
For the normal Codex configuration this is a Codex CLI run hosted by Herdr with
the same grant and MCP boundary as an owner exchange. The response is still
untrusted and passes through the same citation, proposal, scope, budget,
revision, and idempotency checks. It can do exactly one of three useful things:

- append a cited material crew update;
- raise one typed consequential owner decision; or
- freeze a new dependency-aware graph for owner review.

A review never silently executes the graph, changes authority, or impersonates
the owner. Choosing a decision becomes a new visible owner turn; executing a
reviewed graph uses the ordinary canonical effect/receipt path; accepted work is
then scheduled by the deterministic supervisor. Workers continue to communicate
through scoped MCP reports and durable inbox messages, so the observable loop is:

```text
worker report/message -> durable review cursor -> executive review exchange
  -> update | owner decision | reviewed graph
  -> owner-authorized canonical effects -> supervisor -> workers
```

Concurrent worker activity only advances the requested event cut. The current
leased pass completes its frozen cut and another coalesced pass follows; it does
not create one provider call per message. A daemon restart immediately requeues
the lease under the exclusive data-directory lock. Stable operation IDs,
structured-output reuse, owner-turn idempotency, and the reviewed-event cursor
cover crashes before provider completion, after output, after turn persistence,
and before queue completion without duplicate interpretation or duplicate work.

The short-lived project-executive run is control-plane conversation, not worker
delivery. Its provider/runtime failure remains visible on the exact owner turn
and exchange, while the long-lived planning task stays assigned for a later
turn. It does not produce a supervisor resume or failure-acknowledgement approval:
such an approval would change no project work and duplicate the visible exchange
failure.

For a project with an active owner-executive binding and open owner conversation,
M21 also refines M16's conservative generic exception presentation for
implementation runs. An applied
worker blocker, definite failure, repeated failure, stale outcome, or wall-time
exhaustion first becomes executive attention through the coalesced review cursor.
The supervisor does not immediately ask the owner to resume a blocker that has
not been shown resolved, or to acknowledge a failure when acknowledgement changes
no project work. The executive may answer with a cited material update, ask a
specific consequential owner question, or submit a typed recovery/replanning
proposal. Only that exact consequential choice or proposal enters owner review.
CLI-only projects without an owner conversation retain M16's conservative generic
approval behavior.

An uncertain `lost` runtime is the one direct recovery attestation that cannot be
reduced to executive prose. The Decisions surface shows the exact retained run,
diagnosis, and consequence, and requires the owner to confirm that the Herdr pane
or native process has ended before calling `run.lost.resolve`. The UI states that
this confirmation does not stop the process: it transitions the run to failed,
releases retained binding/capacity, and leaves the task blocked for explicit
replanning. Once that exact retirement is recorded and no live run or scheduling
intent retains authority, the executive may propose one `reassign_task` action
that names the blocked task's exact revision and an authorized launch profile.
Accepting and allowing that exact action readies the task and creates a new
scheduling intent; `retry_task` remains reserved for definite `start_failed`
runs. A generic “failure acknowledged” record is not a substitute.

### The owner configures implementation authority explicitly

The Crew page is not read-only decoration. It shows the one durable project
executive separately from implementation workers and exposes one exact owner
configuration operation for adding or disabling a worker. The same operation is
available through `owner.crew.configure` and the secondary `crewfold crew`
command.

Adding a worker creates an enabled agent definition and project-scoped immutable
launch profile, then replaces the executive's manager grant and management launch
profile at the exact binding revision. The new worker can appear in future typed
proposals; it receives no task and starts no run merely because it was added.
Disabling first proves that the worker retains no accepted assignment or live run,
requires a replacement when it is the final worker, removes its profiles from the
replacement grant, and disables it for future work. It never silently stops,
reassigns, or bypasses accepted work. The operation is replay-safe across a lost
response, and the old grant/profile pair is retired in the same binding
reconfiguration transaction so an executive exchange cannot observe mixed
authority.

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
- bounded direct logs or a readable live Herdr activity stream when available.

The daemon adds bounded read-only Git status/diff endpoints rather than returning
unbounded source or storing a source-code copy. Changed paths are observations;
declared claims and accepted outcomes remain separate facts. Raw terminal bytes
are not the default inspection surface: structured provider events are rendered
as readable activity and the exact PTY is retained only in a visibly advanced
protocol console. Raw bytes are labeled operational and untrusted, sanitized
before non-terminal rendering, and never complete a task. Crewfold does not
expose or claim access to
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
  conversation-to-typed-command and receipt model plus one coalesced project
  manager-review cursor.
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
