# Milestone review — workstream execution and context continuity

## Identity

- Milestone: `M23 — Workflow and execution consolidation`
- Review status: `passed`
- Implementation commit: `this M23 milestone commit`
- Reviewer: `automated acceptance plus real subscription-backed browser review`
- Date: `2026-08-19`

## Demonstrable outcome

Crewfold now composes its durable-agent and execution surfaces into one coherent
workstream. An implementation workstream owns one revisioned primary persistent
checkout, a placed durable team, an ordered task graph, and the exact predecessor
outputs required by every successor. Separate provider processes reuse that
existing filesystem; Crewfold does not silently clone, bootstrap, install, or
clean it.

A coordinator submits one inert proposal that freezes the checkout, complete new
team definitions/hierarchy, exact existing-agent revisions, launch profiles,
tasks, dependencies, delivery requirements, and scheduling intents. Proposed new
specialists do not exist before acceptance. Owner acceptance revalidates and
publishes the team and graph together in one transaction. Conversation text is
still not authority.

Dependencies now distinguish completion, handoff, and handoff-with-evidence.
Successors remain unscheduled until the required output exists. Their immutable
context packet contains the bounded predecessor summary, handoff, risks/findings,
checks, changed paths, and evidence references instead of only a terminal status.
Missing or invalid input blocks before provider launch with an attributable
diagnosis and a safe rebuild/relaunch path.

The browser presents the primary checkout, durable team, and topologically ordered
execution chain in one workstream view. Selecting a durable coworker shows its
conversation epochs together with its current or latest task attempt; opening that
attempt exposes readable agent activity, checks, outcome, and exact diagnostics.
An idle conversation therefore no longer hides active or completed work performed
by that same durable identity.

## Real subscription-backed proof

The opt-in browser scenario used a fresh committed repository, a private daemon,
the installed Codex subscription login, Herdr, and headless Chrome. The owner
created two domain-level agents and granted one coordinator. Through its real
Codex thread, the coordinator proposed four inert durable specialists with one
exact work graph and sent its peer one durable message. Explicit browser
acceptance created and placed that team, then ran:

1. `m23-implementer` created `M23_DELIVERY.txt` in the existing checkout;
2. `m23-reviewer` independently inspected the exact predecessor handoff and
   evidence without editing;
3. `m23-remediator` consumed that review output and appended the required
   acknowledgement; and
4. `m23-verifier` consumed the remediation output and verified both delivery
   lines plus the preserved `README.md`.

All four runs used the same canonical checkout path. The browser showed the four
agents under the accepted workstream, the execution chain in dependency order,
zero open tasks, and the reviewer's completed task activity from the reviewer's
durable-agent surface. A peer resumed its own real thread and acknowledged the
durable inbox delivery. Browser reload, native Codex host compaction, and handoff
to a fresh epoch preserved canonical identity and history.

- Scenario: `test/scenarios/domain-agent-live/run.sh`
- Command: `CREWFOLD_RUN_LIVE_CODEX=1 CREWFOLD_SCREENSHOT_DIR=/tmp/crewfold-m23-inert-team ./test/scenarios/domain-agent-live/run.sh`
- Result: `Subscription-backed M23 checkout-bound durable-agent browser acceptance: PASS`

## Accepted boundary

| Area | Accepted behavior |
| --- | --- |
| Workstream checkout | Coordination-only work may omit a primary checkout; source-mutating work requires one exact available writable checkout fixed at workstream creation |
| Domain visibility | Domain-level agents receive bounded read-only observation across attached checkouts; workstream agents default to their workstream primary checkout |
| Team placement | Proposal acceptance atomically places the exact durable agents and launch profiles with the objective/task/intent graph |
| Shared checkout | Sequential reuse is normal; exclusive and claimed work serialize or enforce non-overlap; shared use remains visibly warned |
| Dependency output | Each edge explicitly requires completion, handoff, or handoff with evidence; the complete required predecessor output enters successor context |
| Artifact access | Successors may read only exact evidence referenced by their required predecessor outputs; raw transcripts and private reasoning remain excluded |
| Runtime | Every task attempt revalidates the workstream checkout and current binding, while independent provider processes retain bounded run authority |
| Blockers | Structured diagnosis names the missing input or observed failure and distinguishes in-place resume from fresh context rebuild/relaunch |
| Browser | Domain, workstream, and durable-agent surfaces expose placement, checkout, ordered gating, attached work, and safe actions without decorative empty peers |

## Test evidence

| Suite | Result |
| --- | --- |
| Full Go tree | passed; daemon `58.690s`, loadtest `294.531s`, recovery `32.071s`, Store `105.697s`, all packages green |
| Proposal/team race gate | passed; Store `3.913s`, daemon `3.499s`, covering inert submission, atomic acceptance, strict tool schema, and checkout-bound durable-session context |
| Generated database and web assets | passed; embedded web source hash `2983e866187caa0a7025fde403e928b47a3aa6ed6ea7597995403571425b8b46` |
| Static analysis and whitespace | `go vet ./...`, formatting, and `git diff --check` passed |
| Provider-free browser | authenticated local workbench browser acceptance passed after the M23 UI consolidation |
| Public black-box matrix | every scenario in `scripts/check.sh`, reproducible Linux package, and personal-beta recovery/load passed |
| Real Codex/Herdr browser | passed against the final inert-team-before-acceptance and reviewer-activity assertions |

The unchanged M20 personal-100 profile passed in the full Go tree and again in
the public personal-beta scenario. M23 does not loosen its frozen counts or
resource thresholds.

## Persistence, authority, and recovery

The current baseline owns workstream checkout bindings and dependency-delivery
requirements; no migration or compatibility branch was added. The semantic
registry, full verifier, snapshots, backup, and restore cover the new current
shape. Acceptance is atomic and replay-safe. Checkout/profile/membership drift
before acceptance refuses the entire proposal. Context and placement survive
daemon, Herdr, browser, and provider-epoch replacement.

Names, roles, hierarchy, placement, visibility, and conversation remain
authority-neutral. Effects still require exact current grants, assignment,
claims, checkout policy, capabilities, budgets, and receipts. The browser sees no
provider credential, capability, runtime handle, or private reasoning.

## Known limitations and deferrals

M23 keeps a workstream's primary checkout immutable for that workstream's
lifetime. Changing it requires closing or reviewing a new graph; there is no
silent move of retained work. Workstream completion is not inferred from prose,
nor are objectives auto-cancelled when the last task finishes. Remote control,
multi-user tenancy, hosted synchronization, automatic push/merge/deploy, provider
credential management, and generalized process services remain out of scope.

M24 owns public packaging polish, installation/tutorial material, adapter SDK and
conformance, licensing/governance/security contacts, and the publishable OSS
candidate.

## Decision

- Exit gate satisfied: `yes`
- Product waivers: `none`
- Unresolved HIGH findings: `0`
- Unresolved MEDIUM findings: `0`
- Next milestone entry criteria met: `yes`
- Historical next milestone at review time: `M24 — Public open-source release
  readiness`
- Superseded after the Signal Garden live workflow by
  [ADR-0023](../decisions/0023-operable-workstreams-and-managed-local-services.md):
  M24 now closes operability and managed local services; public release moves to
  M25.
