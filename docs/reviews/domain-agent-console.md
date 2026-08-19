# Milestone review — domain-oriented durable-agent console

## Identity

- Milestone: `M22 — Domain-oriented durable-agent console`
- Review status: `passed`
- Implementation commit: `this M22 milestone commit`
- Reviewer: `automated acceptance plus real subscription-backed browser review`
- Date: `2026-08-19`

## Demonstrable outcome

Crewfold now opens a domain rather than presenting one checkout-bound project as
the organization. Domain Home shows attached checkouts, objective-backed
workstreams, current runs, owner-governed shared knowledge, coordination topics,
and the exact items requiring owner review. The left rail is an arbitrary-depth
tree of durable agents with owner-authored names, roles, operating charters, and
behavior policies. None of those descriptive fields grant authority.

Selecting an agent opens that agent's real resumable Codex app-server thread.
The browser renders owner input, provider messages, repository exploration,
commands, diffs, structured Crewfold client-tool calls, durable message deliveries, and
turn state from the provider event stream. Private reasoning is neither invented
nor filled with placeholder rows. Exact PTY bytes remain an advanced Herdr
diagnostic rather than the primary conversation.

A coordinator with one current owner staffing grant can create domain-level
durable children, delegate only a strict subset of authority it actually holds,
send durable same-domain messages, and submit one inert work proposal. For a
deliverable team the agent supplies only logical names, charters, classes,
assignees, and dependency intent. Crewfold—not the model—selects the current
grant, freezes the checkout, resolves existing memberships/profiles, and derives
bounded budgets and priorities. The owner reviews those exact resolved effects
in Domain Home. Acceptance atomically creates the team, workstream, task graph,
profiles, placements, and scheduling intents. Conversation text—including a
bare `yes`—does not accept the proposal.

The live acceptance used a fresh committed repository, private daemon data,
installed Codex subscription authentication, Herdr, and headless Chrome. The
owner created `orchid` and `fern`, granted `orchid`, and sent one ordinary
instruction. `orchid` read canonical context, sent `fern` one durable message,
and made exactly one successful intent-level proposal call for four nonexistent
specialists and the checkout-bound implement → review → remediate → verify
chain. No grant ID, checkout revision, profile reference, provider/runtime, or
budget came from the model. The browser displayed the frozen checkout, four
inert agents, four assigned tasks, three evidence-carrying dependencies, and the
atomic acceptance effect while still showing only `orchid` and `fern` as real.
Owner acceptance created the team and graph together. All four real Codex runs
completed in dependency order against the same existing checkout; the reviewer
remained read-only and its structured activity was inspectable. `fern` resumed
its own thread and confirmed the delivered message. Browser reload preserved
all identities, conversations, receipts, work history, and hierarchy; native
session compaction plus handoff produced a readable archived epoch and a fresh
current one.

- Public live scenario: `test/scenarios/domain-agent-live/run.sh`
- Exact command: `CREWFOLD_RUN_LIVE_CODEX=1 ./test/scenarios/domain-agent-live/run.sh`
- Observed result: `Subscription-backed M23 checkout-bound durable-agent browser acceptance: PASS`

The broader three-stage implementation/verification/knowledge workflow was also
exercised against a fresh real repository. The coordinator created two
specialists, submitted a three-task graph, and received both child replies as
separate `crewfold_delivery` turns after its owner turn completed. Acceptance
launched implementation, then independent verification, then knowledge
maintenance in dependency order. Tests/build and independent evidence passed;
the sourced knowledge revision remained proposed until the owner accepted its
exact revision and rejected a duplicate. Daemon restart retained three completed
runs and the accepted current knowledge.

## Accepted boundary

| Area | Accepted behavior |
| --- | --- |
| Domain | One current `Project` is presented as a domain; it may span several repositories and checkouts and is not defined by any folder |
| Workstream | One current `Objective` organizes an independently managed outcome; duplicate titles are rejected in the browser while identity remains the canonical ID |
| Agent tree | Same-domain acyclic memberships form arbitrary-depth attention hierarchy; `default` affects navigation only |
| Conversation | One durable agent binds one resumable provider thread; replacement continuity uses canonical Crewfold context when native resume is unavailable |
| Staffing | Owner grants exact provider/runtime profiles, task classes, descendants, concurrency, budgets, and expiry; a manager can allocate or delegate only within that envelope |
| Work proposal | Coordinator supplies logical intent; Crewfold resolves exact current authority; submission is inert; explicit owner acceptance revalidates and atomically publishes the exact team/objective/task/dependency/intent graph |
| Messaging | Same-domain messages are immutable and replay-safe; wakes wait behind an active turn and arrive later with explicit delivery provenance |
| Knowledge | Agent proposals include authenticated primary provenance and remain non-current until exact owner acceptance |
| Source effects | Durable conversation is read-only; implementation/review/verification use exact assigned Crewfold runs and current capabilities |
| Runtime | Herdr hosts normal interactive Codex activity; Direct remains an advanced headless fallback; provider-local helpers are visibly temporary and cannot become durable staff implicitly |
| UI | Terminal-oriented three-pane console, keyboard-safe forms, readable structured event stream, contextual assignment/changes/briefing/verification/staffing, and advanced raw terminal disclosure |

## Persistence and recovery

M22 adds exact current-baseline records for domain memberships, session bindings,
staffing grants/profiles/classes/allocations, domain tool receipts, and domain work
proposals. It extends existing objectives, tasks, dependencies, scheduling
intents, messages, and immutable knowledge revisions rather than adding parallel
authority stores. The installed schema digest and semantic verifier cover all new
tables. Backup remains a quiescent exact database plus referenced immutable
artifacts; provider thread references are node-local operational continuity and
do not authenticate a restored node.

Session, message, proposal, and accepted knowledge continuity were checked after
daemon/browser restart. Child creation and work acceptance are transactional and
idempotent. A wake leased while the destination thread is busy is durably
deferred rather than injected or lost.

## Security and autonomy

- The browser receives no provider credential, node key, capability, runtime
  handle, or full provider-thread binding.
- Role, name, hierarchy, charter, preferred entry, and conversation text remain
  descriptive. Current grants, assignments, claims, budgets, capabilities, and
  exact accepted operations authorize effects.
- Coordinator tools are a closed client-owned tool set. Codex may expose them as
  direct namespace calls or through its code-mode tools object; both forms cross
  the same app-server `item/tool/call` boundary. Calls are strict, replay-safe,
  bounded, and recorded with exact success/failure receipts.
- Child staffing cannot exceed the parent's provider/runtime, task-class,
  descendant, concurrency, budget, expiry, or allocation envelope.
- A work or knowledge proposal has no canonical effect before explicit owner
  acceptance of its exact current revision.
- Durable session checkout access is read-only. Separate task runs receive the
  current run-scoped source capability.
- Subscription-backed testing uses the installed Codex CLI and native login; no
  API key is collected by Crewfold.

## Known limitations and deferrals

The console is owner-local, single-user, and IPv4-loopback only. There is no
hosted control plane, remote browser, organization tenancy, mobile client,
automatic push/merge/deploy/publication, or credential store. Domain services
remain canonical resources exposed by current APIs rather than a general
process-orchestration marketplace. Provider-private reasoning is not available.
Codex is the rich durable-session implementation in M22; other providers retain
their existing adapter boundaries until a matching rich-session conformance path
is implemented. M23 now closes the workstream execution-home and dependency-output
gaps found during live use; public packaging, adapter SDK polish, tutorials,
license, and release publication belong to M24.

## Decision

- Exit gate satisfied: `yes`
- Product waivers: `none`
- Unresolved HIGH findings: `0`
- Unresolved MEDIUM findings: `0`
- Next milestone entry criteria met: `yes`
- Next milestone: `M23 — workflow and execution consolidation`
