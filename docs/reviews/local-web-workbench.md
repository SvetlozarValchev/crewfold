# Milestone review — local web workbench

## Identity

- Milestone: `M21 — Local web workbench`
- Review status: `passed`
- Implementation commit: `6116b5d0b21bfba48007da464ff3bae0c8543416`
- Herdr-first runtime correction: `a6a8f82`
- Event-driven manager-loop completion: `d1b5dd8`
- Durable project-executive rework: `current M21 implementation checkpoint`
- Reviewer: `automated acceptance and real-browser review`
- Date: `2026-08-17`

## Demonstrable outcome

Crewfold now starts as one owner-local Linux service and opens one embedded web
workbench. From an empty exact-current installation, the browser can register a
committed repository, select a provider/runtime, create a workspace and project,
send an ordinary-language instruction to one durable provider-backed project
executive, review the executive's exact typed proposal, accept that exact
revision, inspect the resulting crew and repository observation, and read
decisions, evidence, activity, health, and a bounded project briefing.

Conversation is presentation rather than hidden authority. Each instruction is
persisted before a short-lived executive run receives its frozen canonical
context through scoped MCP. The run may answer, clarify, or submit only the
existing closed manager-proposal grammar. No graph effect occurs until the owner
accepts the displayed proposal, at which point current scope, grant, revisions,
budget, and policy are checked again and exact durable receipts are recorded.
Stale canonical state disables unsafe browser controls.
Herdr supplies the normal interactive runtime and live stream; Crewfold renders
structured provider events for ordinary inspection and keeps the exact protocol
PTY behind a visibly advanced console. Crewfold's database, events, receipts,
reports, and evidence remain canonical truth. Direct
remains an explicit advanced CI/headless fallback.

The same project executive is event-driven as well as owner-invoked. Structured worker
reports and agent messages atomically advance one durable project review cursor.
The daemon coalesces them, runs another scoped executive exchange through
Codex/Herdr in normal use, and appends a cited update, one typed owner question,
or an inert typed proposal. An owner decision or proposal acceptance returns to
the same canonical receipt and supervisor path; it is not browser-only
choreography.

- Public scenario: `test/scenarios/web-workbench-shell/run.sh`
- Exact command: `./test/scenarios/web-workbench-shell/run.sh`
- Observed result: `Authenticated local web workbench browser acceptance: PASS`

## Accepted boundary

| Area | Accepted behavior |
| --- | --- |
| Service/open | Private XDG paths, idempotent Crewfold plus installed-Herdr companion user services, one-time fragment bootstrap, and no model call during installation/open |
| Browser security | Exact IPv4 loopback Host/Origin, HttpOnly SameSite session, in-memory CSRF, CSP/no-frame policy, strict bounded RPC, and SSE used only to invalidate canonical reads |
| Onboarding | Browser-only workspace/project/checkout/provider/runtime setup against an existing committed Git repository; live Herdr is the interactive default and Direct is the explicit advanced CI/headless fallback |
| Conversation | Bounded durable owner/executive exchanges run through one visible provider-backed executive agent, frozen canonical context, replay-safe idempotency, and no prose-derived authority |
| Manager loop | Worker reports/messages trigger one coalesced restart-safe exchange with the same executive at an exact event cut; updates, owner questions, and typed proposals never silently execute |
| Planning | The executive may submit only the existing closed manager-proposal grammar; Decisions shows every exact operation and acceptance revalidates current scope, grant, revisions, and budget |
| Execution | Objective, task, assignment, and run effects use existing Store mutations and retain exact method/request/response/event receipts; rejected completion exposes an exact-revision retry that preserves the prior review and atomically queues a fresh context-bound run |
| Inspection | Work graph, crew, inbox, decisions, evidence, activity, briefings, health, Git observation, and logs come from current daemon/Store truth |
| Terminal | A 30-second single-use WebSocket grant is bound to one browser session, workspace, current-node run, and PTY; the default view renders readable live events while exact PTY bytes and direct input remain an advanced disclosure; no handle or capability is exposed |
| UI | React/Vite assets are pinned and embedded; Lucide supplies named SVG icons; responsive navigation, labels, reduced motion, and stale-state fences are explicit |

## Test evidence

| Suite | Command | Result |
| --- | --- | --- |
| Real Chrome workflow | `./test/scenarios/web-workbench-shell/run.sh` | passed |
| Full Go tree | `./scripts/go.sh test ./... -count=1` | passed; loadtest 274.910s, Store 90.533s, daemon 52.326s |
| Durable executive path | full-daemon browser exchange, exact replay, executive-only context tools, typed proposal acceptance, standing planning assignment, and a second distinct exchange | passed |
| Event-driven loop | focused Store and full-daemon executive review, structured-output replay, and recovery quiescence tests | passed normally and with the race detector |
| Focused race packages | execution and local API packages plus exact M21 Store/daemon executive tests | passed |
| Embedded database generation | `./scripts/check-generated-db.sh` | passed |
| Embedded web assets | `./scripts/build-web.sh` | passed; content hash `fd09e87af800d55c4891729df1d56b17ca4e6b1aca21c7c7fb073e4b4c954f96` |
| Static analysis | `./scripts/go.sh vet ./...` | passed |
| Linux candidate | `./scripts/package-linux_test.sh` | passed and reproducible |
| Frontend build | `./scripts/build-web.sh` | TypeScript and Vite passed |
| Formatting/syntax/whitespace | Go formatting scan, `node --check`, `sh -n`, and `git diff --check` | passed |

The unchanged M20 `personal-100` test passed inside the full repository gate. Its
frozen counts, thresholds, and implementation were not altered by the executive
rework.

## Persistence, authority, and security

- Owner conversations are bounded audited exchange envelopes, not a second
  journal. Existing objectives, tasks, assignments, proposals, runs, approvals,
  events, and immutable receipts remain authoritative.
- The owner does not select `query`, `plan`, or `act`. The executive classifies
  its response after reasoning; prose alone creates no domain effect.
- Only the exact bound executive launch profile receives the two executive MCP
  tools. Ordinary managers retain only the proposal tools granted by their exact
  owner-authored manager grant.
- Executive proposals remain inert and become stale when their frozen event
  high-water or authority revisions no longer match.
- The browser never receives provider credentials, node keys, MCP capabilities,
  runtime bindings, launch environments, or terminal handles.
- Repository inspection is fresh, bounded, UTF-8/control-safe, scope-labeled, and
  not persisted as source or diff text in conversation history.
- Role and purpose remain descriptive. Browser prose cannot grant authority,
  raise paid-cost authority, bypass capacity, or invent a new operation type.
- A `review` run paired with an exact `changes_requested` task may be retried only
  from the displayed run and task revisions. The retained assignment is reopened
  in the same transaction as the fresh run request; stale or superseded review
  controls produce no task, event, context, capability, or runtime effect.

## Known limitations and deferrals

Provider-free acceptance intentionally uses a deterministic implementation of
the same executive MCP contract; normal Codex projects use the installed,
subscription-authenticated Codex CLI for owner and automatic executive turns.
The executive sees bounded canonical state and
read-only repository scope, not private model reasoning or unbounded transcripts.
Instructions that request destructive, publication, external-communication,
credential, network, authority-changing, or cost-escalating work fail closed and
must be expressed through an existing exact reviewed operation surface. The web
surface is owner-local only: there is no remote bind, multi-user identity, hosted
control plane, mobile app, provider transcript/reasoning view, browser credential
store, automatic push/deploy/publication, or direct browser database access.

M22 owns public packaging and installation polish, adapter SDK/conformance,
tutorial/example content, security/governance contacts, license selection, and
release publication.

## Decision

- Exit gate satisfied: `yes`
- M21 product waivers: `none`
- Validation note: `full tree, unchanged M20 personal-100, installed doctor, and real-browser flow passed`
- Unresolved HIGH findings: `0`
- Unresolved MEDIUM findings: `0`
- Next milestone entry criteria met: `yes`
- Next milestone started: `no`

M21's initial workbench foundation is recorded at
`6116b5d0b21bfba48007da464ff3bae0c8543416`, with the Herdr-first service,
preflight, diagnosis, and retry correction in `a6a8f82`, and the initial
event-driven loop in `d1b5dd8`. This review now accepts the durable
project-executive rework as the sole current owner-conversation path. M22 remains
the next milestone and was not started during this review.
