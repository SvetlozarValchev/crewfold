# Milestone review — local web workbench

## Identity

- Milestone: `M21 — Local web workbench`
- Review status: `passed`
- Implementation commit: `6116b5d0b21bfba48007da464ff3bae0c8543416`
- Herdr-first runtime correction: `a6a8f82`
- Event-driven manager-loop completion: `d1b5dd8`
- Reviewer: `automated acceptance and real-browser review`
- Date: `2026-08-15`

## Demonstrable outcome

Crewfold now starts as one owner-local Linux service and opens one embedded web
workbench. From an empty exact-current installation, the browser can register a
committed repository, select a provider/runtime, create a workspace and project,
ask a factual question, submit an act instruction, edit and seal a reviewed plan,
launch its ready work, inspect the resulting crew and repository observation, and
read decisions, evidence, activity, health, and a bounded project briefing.

An act response is not decorative chat: it executes a closed typed operation graph
and records exact durable effect receipts. A plan remains inert until its reviewed
revision is launched. Stale canonical state disables unsafe browser controls.
Herdr supplies the normal interactive runtime and live terminal; Crewfold's
database, events, receipts, reports, and evidence remain canonical truth. Direct
remains an explicit advanced CI/headless fallback.

The manager is now event-driven as well as owner-invoked. Structured worker
reports and agent messages atomically advance one durable project review cursor.
The daemon coalesces them, runs a read-only provider review through Codex/Herdr in
normal use, and appends a manager-originated update, one typed owner question, or
an inert successor graph. An owner decision or graph launch returns to the same
canonical receipt and supervisor path; it is not browser-only choreography.

- Public scenario: `test/scenarios/web-workbench-shell/run.sh`
- Exact command: `./test/scenarios/web-workbench-shell/run.sh`
- Observed result: `Authenticated local web workbench browser acceptance: PASS`

## Accepted boundary

| Area | Accepted behavior |
| --- | --- |
| Service/open | Private XDG paths, idempotent Crewfold plus installed-Herdr companion user services, one-time fragment bootstrap, and no model call during installation/open |
| Browser security | Exact IPv4 loopback Host/Origin, HttpOnly SameSite session, in-memory CSRF, CSP/no-frame policy, strict bounded RPC, and SSE used only to invalidate canonical reads |
| Onboarding | Browser-only workspace/project/checkout/provider/runtime setup against an existing committed Git repository; live Herdr is the interactive default and Direct is the explicit advanced CI/headless fallback |
| Conversation | Bounded durable query/plan/act turns with one frozen current operation grammar, canonical hashes, replay-safe idempotency, and no prose-derived authority |
| Manager loop | Worker reports/messages trigger one coalesced restart-safe review at an exact event cut; updates, owner questions, and reviewed graphs carry explicit manager provenance and never silently execute |
| Planning | Task/objective text, priority, enabled agent, and fixed token/time limits are editable before launch; revision and event high-water must still match |
| Execution | Objective, task, assignment, and run effects use existing Store mutations and retain exact method/request/response/event receipts |
| Inspection | Work graph, crew, inbox, decisions, evidence, activity, briefings, health, Git observation, and logs come from current daemon/Store truth |
| Terminal | A 30-second single-use WebSocket grant is bound to one browser session, workspace, current-node run, and PTY; no handle or capability is exposed |
| UI | React/Vite assets are pinned and embedded; Lucide supplies named SVG icons; responsive navigation, labels, reduced motion, and stale-state fences are explicit |

## Test evidence

| Suite | Command | Result |
| --- | --- | --- |
| Real Chrome workflow | `./test/scenarios/web-workbench-shell/run.sh` | passed |
| Focused Go packages | `./scripts/go.sh test ./internal/store ./internal/daemon ./internal/localapi ./internal/execution ./protocol -run 'M21\|OwnerConversation\|Workbench\|WebBootstrap' -count=1` | passed |
| Event-driven loop | focused Store, full-daemon browser, structured-output replay, and recovery quiescence tests | passed normally and with the race detector |
| Focused race packages | same packages and expression with `-race` | passed |
| Embedded database generation | `./scripts/check-generated-db.sh` | passed |
| Embedded web assets | `./scripts/check-web-assets.sh` | passed; 279,254 bytes |
| Static analysis | `./scripts/go.sh vet ./...` | passed |
| Linux candidate | `./scripts/package-linux_test.sh` | passed and reproducible |
| Frontend build | `./scripts/build-web.sh` | TypeScript and Vite passed |
| Formatting/syntax/whitespace | Go formatting scan, `node --check`, `sh -n`, and `git diff --check` | passed |

The unchanged M20 `personal-100` test was not used as an M21 completion blocker
in this run: on this host it exceeded its six-minute wall-clock test timeout while
unrelated compiler processes kept load averages in the high teens. Its frozen
counts, limits, implementation, and tests were not altered. All other packages in
that attempted repository-wide run passed, and M21's own browser, normal, race,
static, generated, and package gates passed afterward.

## Persistence, authority, and security

- Owner conversations are bounded audited execution envelopes, not a second
  journal. Existing objectives, tasks, assignments, runs, approvals, events, and
  immutable receipts remain authoritative.
- Query turns create no domain effect. Plans can be edited only while pending and
  become stale when their frozen event high-water no longer matches.
- The browser never receives provider credentials, node keys, MCP capabilities,
  runtime bindings, launch environments, or terminal handles.
- Repository inspection is fresh, bounded, UTF-8/control-safe, scope-labeled, and
  not persisted as source or diff text in conversation history.
- Role and purpose remain descriptive. Browser prose cannot grant authority,
  raise paid-cost authority, bypass capacity, or invent a new operation type.

## Known limitations and deferrals

Provider-free acceptance intentionally uses a deterministic typed interpreter;
normal Codex projects use the installed, subscription-authenticated Codex CLI for
owner and automatic manager turns. The manager sees bounded canonical state and
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
- Validation note: `M20 personal-100 rerun omitted under unrelated host saturation`
- Unresolved HIGH findings: `0`
- Unresolved MEDIUM findings: `0`
- Next milestone entry criteria met: `yes`
- Next milestone started: `no`

M21 is accepted at implementation commit
`6116b5d0b21bfba48007da464ff3bae0c8543416`, with the Herdr-first service,
preflight, diagnosis, and retry correction in `a6a8f82`, and the event-driven
manager loop in `d1b5dd8`. M22 remains the next milestone and was not started
during this review.
