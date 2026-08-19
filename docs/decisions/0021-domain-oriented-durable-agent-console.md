# ADR-0021: Domain-oriented durable-agent console

- Status: accepted
- Date: 2026-08-18
- Supersedes: the singular short-lived project-executive and page-oriented
  interaction model in [ADR-0020](0020-local-web-workbench.md)
- Superseded in part by: the workstream execution-home, atomic placement,
  dependency-output, and unified-timeline contract in
  [ADR-0022](0022-workstream-execution-and-context-continuity.md)

## Context

ADR-0020 established the correct deployment and trust boundary for an owner-local
web workbench: the browser is a client, the Go daemon owns canonical state and
authority, Herdr owns live terminals, and MCP is the structured agent surface.
Its implemented interaction model is too narrow, however. It binds one executive
to one project, invokes a short-lived one-response provider run for each owner
turn, presents implementation agents as a flat list, and spreads understanding
across separate administrative pages.

Real owner work does not follow repository or checkout boundaries. A single
engineering body such as World Engine can have terrain consolidation, prefab
authoring, asset cooking, and rendering work proceeding in different checkouts.
The workstreams are independently useful but share formats, constraints,
upstream changes, and owner knowledge. Conversely, one coordinated product can
span several repositories. Treating a checkout as the organizational unit makes
relevant knowledge local to the wrong folder and hides coordination that should
cross workstreams.

Real Codex use also exposes a hierarchy that the current workbench discards. A
long-lived owner-facing session commonly delegates bounded research,
implementation, review, and validation to child agents. Some children are
temporary provider-local helpers; others have independent objectives, need direct
owner communication, coordinate their own children, or should survive the
provider process that created them. Those latter agents are durable Crewfold
actors, not invisible transcript details.

Crewfold's existing model already supplies most of the required kernel: a project
can span repositories and checkouts; objectives and tasks separate intent from
filesystem location; agent definitions survive runs; Herdr hosts provider
sessions; messages, meetings, claims, knowledge, checks, evidence, outcomes, and
MCP are durable coordination boundaries. The missing product boundary is a
domain-oriented organization and a session-first interface over those records.

## Decision

### Project scope is presented as a domain

The primary owner scope below the local workspace is a **domain**: a durable body
of related engineering knowledge, policy, workstreams, repositories, services,
and agents. The current canonical `Project` record is the initial persistence and
API backing for a domain. The owner interface calls it a domain because a domain
is explicitly not a repository, checkout, branch, folder, or single job.

An `Objective` is presented as a **workstream** when it names an independently
managed outcome inside a domain. A workstream may use one or more attached
repositories/checkouts and may share those resources with other workstreams under
the existing checkout write modes and claim rules.

For example:

```text
World Engine domain
├─ terrain consolidation workstream
├─ prefab system workstream
├─ asset pipeline workstream
└─ libweb/layout workstream
```

`world-engine`, `world-engine-4`, and `world-engine-5` are attached checkouts.
They are not parent scopes and do not determine agent hierarchy.

### Durable agents form an owner-visible attention tree

Each durable agent may have zero or one current manager relationship inside a
domain and any number of durable children. The relationship organizes attention,
delegation, roll-up, and navigation. It grants no authority by itself. Existing
owner-authored grants, profiles, task assignments, checkout policy, claims,
budgets, and capabilities remain the authority boundary.

Every membership also freezes an owner-reviewed operating charter and one
behavioral policy: `hands_on`, `adaptive`, or `delegation_first`. These are real
provider instructions, not decorative role labels. A delegation-first agent is
instructed to use a current staffing grant to propose the durable team and work
graph it needs before absorbing implementation itself, or to explain the missing
grant. Immediate child creation is reserved for explicit continuing domain-level
staff outside a deliverable proposal. The charter and policy still grant no task,
checkout, staffing, budget, or effect authority.
Changing them is a revision-checked membership mutation.

The durable conversation itself is read-only coordination and inspection.
`hands_on` means the selected agent performs its own analysis, planning, and
owner communication rather than delegating those responsibilities by default;
it does not turn conversation text into checkout authority. Repository effects
belong to an exact assigned Crewfold run with its frozen checkout, claims,
capabilities, policy, budget, and receipts.

The owner interface may provide fixed, descriptive starting templates for common
responsibilities. Selecting one only prefills the editable owner intent used to
draft or author the charter. Templates are not a role taxonomy, do not grant
authority, and coexist with an always-available custom option.

The product may use a separate read-only ephemeral Codex thread to draft a
candidate name, role, charter, and policy from owner intent. That helper exposes
no Crewfold tools, uses `approvalPolicy=never`, records no Crewfold domain state,
and returns one closed typed object for owner review. It is not the durable agent,
does not choose authority, and is discarded after the draft.

The tree is not restricted to executive and worker levels. A domain steward may
coordinate workstream leads; a lead may coordinate implementers, independent
reviewers, and scenario-specific testers; any of those agents may coordinate a
narrower durable child when its staffing grant allows it.

Creating a durable child is a typed, receipted Crewfold operation. An owner may
grant a manager a bounded staffing envelope containing permitted domain,
provider/runtime profiles, maximum descendants, maximum concurrency, budgets,
and eligible task classes. Creation inside that envelope does not require a
redundant owner decision. Expansion beyond it, provider credential changes,
external publication, destructive effects, and authority/budget changes stop at
the existing owner boundary.

A granted coordinator may also submit one **inert work proposal**. The proposal
names one workstream objective, its complete proposed durable team/hierarchy, and
a bounded task/dependency graph. New team members are proposal-local definitions;
existing domain staff are exact revisioned references. Submission creates no
agent, membership, launch profile, objective, task, assignment, run, checkout
claim, or provider session. The owner reviews one immutable proposal revision in
the browser. Acceptance atomically creates and places the team together with the
objective, tasks, dependencies, and scheduling intents; the deterministic
supervisor starts only ready work and leaves dependent implementation, review,
verification, and knowledge-maintenance work visibly gated. Conversation text,
including a bare “yes”, is never that acceptance.

This is the normal end-to-end delegation path. A coordinator with a current
staffing grant plans its durable specialists and their bounded responsibilities,
submits that inert team and graph, and observes them only after exact acceptance.
It does not ask the owner to reproduce agent definitions, task records, or start
each child manually.
The grant is delegable authority only inside its frozen envelope: a child may
receive a narrower descendant grant from authority the parent actually holds,
but no parent can manufacture provider, runtime, task-class, budget, concurrency,
checkout, or source-effect authority that it was not given.

Task classes are exact lowercase authority labels, but the owner interface
presents the common implementation, review, verification, coordination,
knowledge-maintenance, and integration classes as explained selections. A custom
class remains available for domain-specific work. Each token, cost, and time
budget dimension can be finite or unlimited. The existing canonical value zero
means unlimited: an unlimited child allocation is valid only beneath an
unlimited parent dimension, while finite allocations remain cumulatively
bounded.

Durable organization is retired, never erased. Retiring an agent membership
removes it from the active hierarchy while preserving its definition,
conversation, receipts, assignments, runs, and events as history. The Store
refuses retirement while the agent has active children, nonterminal assigned
work, unresolved runs, or active staffing grants. Cancelling an objective-backed
workstream likewise preserves history and never cascades to its tasks or agent
placement. The web console conservatively withholds that organizational action
while scoped agents or nonterminal tasks remain, keeps those prerequisites
visible, and moves successful transitions into a compact retired/closed history
rather than pretending to delete canonical records.

Provider-local subagents remain distinct. A bounded helper used for one turn may
be rendered in its parent's activity but is not automatically promoted to a
durable Crewfold identity. A child becomes durable when it owns a continuing
objective or task, needs direct communication, coordinates others, owns a
resource or service, or must survive the current provider run.
Provider-local helpers may perform bounded private research inside one turn, but
must not replace implementer/reviewer/verifier identities named by a durable
staffing plan, receive continuing source responsibilities, or be reported as
Crewfold staffing. The parent session must expose their provider-local lifecycle
as such.

### A durable agent owns one logical conversation with replaceable provider epochs

The normal Codex experience gives a durable Crewfold agent one logical owner-facing
conversation. That logical conversation may contain several immutable provider
epochs over the agent's lifetime. Selecting the agent opens that actual lineage,
not a one-shot form interpreter and not an immortal provider process. Owner input
is delivered only to the current epoch; structured provider events, tool calls,
diffs, approvals, responses, epoch boundaries, and attached execution activity
are rendered as one primary activity stream.

Persistence does not require a provider process or provider context to consume
resources forever.
An agent may be `idle`, `starting`, `working`, `waiting`, `blocked`, or `failed`.
When needed, Crewfold resumes the provider conversation through the provider's
structured session transport. Herdr remains the interactive host for separately
authorized execution runs; it is not a second implementation of the owner-facing
Codex conversation. If provider continuity is unavailable,
Crewfold starts a replacement run from the agent's canonical task, messages,
accepted knowledge, handoff, and evidence. The Crewfold agent identity survives
either outcome.

Provider conversation state is continuity and presentation, never authority or
canonical project truth. The daemon still validates every structured mutation;
private chain-of-thought and provider-private state are neither required nor
claimed.

Owner turns and durable message wakes are serialized per provider thread. If a
child reply arrives while the coordinator is still handling owner input,
Crewfold leaves that wake pending and retries after the active turn settles. The
reply becomes a separate provider turn with explicit `crewfold_delivery`
provenance; it is never spliced into the middle of the owner's instruction.

The current app-server epoch resumes with `sandbox=read-only`, including after a
daemon/provider restart. It may inspect the selected checkout and use the closed
audited Crewfold tool set described below. It may not edit that checkout merely because the
owner said “yes” in conversation. A separately assigned Crewfold execution run
is the only current path for implementation, review, or verification effects.

An execution run is an auditable authority and environment attached to the same
durable agent, not a second owner-facing agent identity. The Session surface must
show its lifecycle, commands, changes, messages, blockers, and verification in
the same logical timeline. If provider isolation requires a second process or
thread, that is an implementation detail labelled as an attached execution
environment; it must not leave the durable agent apparently idle while hidden
work occurs elsewhere.

Provider epochs are replaceable. A normal rotation happens only between turns,
with no unresolved provider request or execution effect. Crewfold freezes the old
epoch, records a bounded mechanical handoff from canonical assignments, runs,
claims, checkouts, inbox, accepted knowledge, decisions, changed-path observations,
and verification gaps, and may append an attributed outgoing-agent narrative.
It then starts one fresh current epoch for the same durable agent. Old epochs stay
read-only and lazily inspectable; their tool receipts, proposals, messages, and
knowledge provenance remain bound to the exact historical thread.

Rotation may be owner-requested or recommended from bounded provider health such
as process RSS, turn/token count, transcript bytes, age, idle time, or repeated
degradation. Thresholds are owner policy, not authority. Crewfold never silently
rotates during an active turn or live execution. An emergency provider kill is
reported honestly and any affected execution remains unknown/lost until its
ordinary recovery contract resolves it.

For Codex, M22 uses the provider's rich-client protocol rather than reproducing a
chat harness. Each active durable agent has a lazily started, resource-bounded,
private app-server host managed by the daemon. No two durable agents share that
process. The daemon connects over its private local transport and binds one
non-ephemeral Codex thread id to each provider epoch.
Selecting an agent reads its current lineage. Sending owner input resumes the
current thread and starts or steers a real Codex turn. After a terminal turn, the
disposable app-server exits following a short observation grace; the next owner or
message turn resumes the same persisted epoch in a fresh process. Native Codex
compaction keeps that epoch while compacting its provider context and recycling
the host. Rotation instead starts a new epoch from the canonical handoff. Full
provider thread ids are operational bindings and are not presented as domain
identity.

The current `codex exec --ephemeral` integration is explicitly not this surface.
It remains historical M21 worker/executive behavior and must not be relabeled as
a durable session. M22 removes `--ephemeral` from the durable-agent path and uses
app-server thread lifecycle, streamed item events, and required Crewfold MCP
configuration. There is one primary Codex conversation integration path in M22.
Raw Herdr/TUI attachment is an advanced diagnostic view of an attached execution
run and never masquerades as the app-server conversation.

The Session renderer is a faithful adapter with a closed source set:

- exact owner input accepted for the selected Codex thread;
- exact Codex user and agent message items;
- exact provider lifecycle, plan, command, file-change, web-search, and MCP item
  events, including their actual completion or failure state; and
- exact Crewfold MCP requests and receipted results, clearly labeled as
  Crewfold-owned effects.

The renderer may sanitize terminal controls, apply bounded truncation with an
explicit continuation, and format structured fields for readability. It may not
paraphrase, merge, infer, or insert an agent-sounding status narrative. Derived
scope, dependency, change, briefing, verification, and outcome projections stay
in their named views with source links. In particular, a card such as “scope
remains isolated” is invalid inside Session unless those are the exact words of
the owner or Codex; Crewfold's independent scope assessment belongs in
Assignment or Changes.

### Knowledge is domain-scoped and routed by relevance

Accepted decisions, constraints, findings, runbooks, briefs, risks, interface
contracts, and summaries belong to the domain independently of checkout paths.
Source documents, commits, PRs, artifacts, screenshots, and external owner input
are linked evidence rather than parallel folder-specific truth.

Agents receive bounded relevant domain context. A prefab storage decision can be
routed to terrain and asset-pipeline workstreams without broadcasting every
transcript. A terrain requirement can trigger compatibility review by the cooker
agent. Owner knowledge obtained from coworkers is recorded with owner provenance
and can be proposed or accepted through the same knowledge-governance boundary.

Review and testing are first-class independent duties. An implementer may write
the change; a reviewer receives the contract, diff, and evidence without
inheriting the implementer's private provider context; a scenario tester receives
a domain-specific charter and may operate the product through an explicit MCP or
managed service. Their findings and evidence roll up to the workstream and domain
without being flattened into the implementer's self-report.

### The web workbench becomes a rich agent console

The owner-local web deployment, daemon authority, browser security, embedded
frontend, Herdr runtime, and MCP boundaries from ADR-0020 remain in force. The
information architecture changes:

- **Left rail:** domains and expandable durable-agent trees, with workstream,
  status, unread, blocker, and owner-attention indicators.
- **Center:** the selected agent's real resumable provider conversation and
  readable structured activity. Selecting a domain instead opens its curated
  overview rather than a synthetic executive chat.
- **Context rail:** the selected scope's objective, checkout/resources, children,
  messages, current changes, checks, evidence, services, decisions, and capacity.
- **Domain workspace:** shared knowledge, workstreams, cross-workstream interface
  changes, upstream impact, services, documents, and owner decisions.
- **Advanced console:** exact PTY bytes remain separately available for diagnosis
  and direct terminal input, but are not the default agent experience.

The console ships with a small set of accessible terminal-oriented defaults and
allows the local owner to select one user-authored theme. Theme selection is an
owner-local presentation preference: it cannot change domain state, agent
authority, evidence meaning, or the semantic colors and labels required to
distinguish working, waiting, blocked, failed, and owner-attention states.

The domain overview has a small stable shell and a domain-authored home. The
stable shell contains domain identity, the canonical cut, workstream/agent and
resource counts, genuine owner attention, and navigation to durable agents. The
home is an ordered, revisioned set of pins to existing canonical knowledge,
documents, external sources, managed surfaces, and workstream projections, plus
one bounded Markdown orientation note. The note is always labeled with its
author, revision, update time, and linked sources; it is never presented as a
daemon-derived fact or as unattributed model memory.

An owner or explicitly authorized knowledge maintainer may revise the home.
Rendered Markdown is sanitized and cannot contain scripts or create an alternate
authority surface. M22 does not add arbitrary per-domain executable widgets.
Domain-specific behavior comes from pinning real resources and managed surfaces:
a game-engine domain may pin a test world and cooker watch, while a cloud domain
may pin deployment runbooks and local emulators.

The overview is conditional rather than a fixed enterprise dashboard.
Cross-workstream attention appears only for explicit multi-workstream interface,
risk, dependency, message, or upstream-source records. Managed surfaces,
knowledge collections, and workstream roll-ups disappear when none exist. Empty
domains remain honest and small. Any authored synthesis stays attributed and
links back to the records it summarizes.

The selected-agent center uses five literal views rather than generic dashboard
categories:

- **Session** is the durable provider conversation and structured activity;
- **Assignment** is the canonical objective/workstream, task contract,
  dependencies, checkout/resources, claims, budget, policy, and child work;
- **Changes** is one bounded Git observation cut with exact changed paths and
  bounded diffs, compared with the run, checkout, declared claims, and completion
  report. A shared checkout is never presented as proof that one agent authored
  every observed byte;
- **Briefing** is the immutable context packet plus acknowledged live context
  deltas, accepted knowledge revisions, and messages actually delivered through
  Crewfold. It describes supplied context, not private model memory; and
- **Verification** separates self-report, mechanical checks, independent review,
  policy acceptance, artifacts, freshness, and unresolved evidence gaps.

Most source records already exist: tasks and assignments, context packets and
deltas, Git dirty-path observations and claim drift, completion payloads, check
runs and artifacts, evidence classes, freshness, handoffs, and timelines. M22
adds the domain/agent projections, revisioned domain-home pins and note, bounded
diff transport, durable-session binding, and browser composition needed to make
those records usable together.

These views are read models at one declared canonical event cut, not browser
inferences and not agent-authored summaries:

- **Session** joins the durable agent/session binding, owner input, normalized
  provider events, Crewfold tool-call receipts, and run lifecycle. The provider
  owns its words; Crewfold owns their binding and recorded effects. Conversation
  text is never canonical authority.
- **Assignment** joins the current objective, task, dependency/readiness graph,
  assignment, launch profile, checkout, claims, policy, budget, run, and durable
  child delegation. The local owner creates these records directly or accepts a
  bounded manager proposal; the scheduler may execute them but does not invent
  their scope.
- **Changes** compares the checkout and base/terminal Git observations with
  declared claims, claim drift, the bound run, and the agent's completion
  payload. Git owns the observed file state; the agent owns only its self-report.
  Attribution is explicit and may be `isolated-bound`, `shared-observed`, or
  `unattributed`; the UI never turns temporal correlation into authorship.
- **Briefing** renders the immutable context packet and acknowledged context
  deltas selected by the daemon from the exact assignment, dependencies,
  checkout, inbox, participant threads, accepted knowledge revisions, policy,
  grants, omissions, and byte budgets. Knowledge and messages retain their own
  authors; Crewfold owns selection and delivery. This is distinct from the
  project-level outcome briefing and from private provider memory.
- **Verification** has no single owning agent. The owner authors exact criteria
  and allowlisted check definitions; the check worker owns process outcomes,
  artifacts, and Git observations; freshness is deterministically derived from
  repository cuts; an independently assigned reviewer owns its findings; an
  implementation agent owns only its self-report; and owner or explicit policy
  owns acceptance. Crewfold preserves those lanes and derives each requirement's
  `missing|running|verified|failed|stale|unknown` state without turning a passing
  check into task completion or policy acceptance.

The owner-facing language keeps three concepts distinct. A **workstream goal** is
the intended result of the canonical objective. An **assignment** is the exact
current responsibility delegated to one agent. An **accepted outcome** is a
retrospective delivery assessment backed by evidence. The UI never labels an
intended goal as an outcome or implies that assignment activity is accepted
delivery.

Coordination threads remain a fourth, deliberately narrower concept. They are
durable addressed conversations between agents or the owner, not assignments,
progress records, verification evidence, or accepted knowledge. Domain Home
shows a collapsed project-scoped index and each agent Session shows only threads
in which that agent actually participates. A durable-agent tool call must choose
explicitly between continuing an existing thread and opening a genuinely new
topic; omission never silently creates another thread.

Likewise, one selected agent has one **Session** surface: its logical provider
conversation across immutable epochs. An **active run** is the current bounded
execution authority and environment attached to that agent,
and the **advanced terminal** is a raw Herdr PTY attachment to that run. The UI
does not call the terminal a second “live session.” Stopping the current run does
not delete the durable agent or its conversation history.

Canonical tasks, decisions, evidence, activity, and health remain accessible, but
they are contextual views and drill-downs rather than ten peer destinations the
owner must reconcile manually. The owner may address any durable agent directly.
One preferred agent may be the default entry point, but the schema and UI
do not enforce one executive per domain or one agent per checkout.
“Preferred” means only which agent the client opens by default. It is rendered as
`default`, not `entry`, and has no rank or authority semantics. Onboarding has no
hardcoded `lead`, `domain-steward`, or other magic identity.

The built M22 console and its strict public schemas are the accepted interaction
reference. The exploratory standalone HTML mock was deleted after the information
architecture was implemented so it cannot drift into a second product contract or
an alternate authority surface.

### M22 proves the corrected owner experience

M21 remains historical evidence for the service, browser security, canonical
transport, Herdr stream, and first web implementation. M22 replaces its singular
executive interaction with the domain-oriented durable-agent console. ADR-0022
subsequently assigns workstream execution and context consolidation to M23 and
moves public OSS readiness to M24; Crewfold is not release-ready while its primary
workflow remains compositionally incomplete.

## Consequences

- Related work can share governed knowledge without sharing a checkout or being
  forced into one sequential job.
- Existing long-running Codex workstream sessions can become visible durable
  agents under one domain instead of being reconstructed from folder names.
- Multiple orchestrators are allowed. A default agent improves navigation but
  is a presentation choice, not an authority or cardinality constraint.
- More provider session lifecycle and structured-stream integration is required;
  the current one-response executive harness is removed from the normal path.
- A durable parent/child relationship, staffing grant, provider-session binding,
  domain navigation API, and scoped knowledge-routing projection become new
  persisted/public seams and require exact restart, authorization, and cycle
  tests.
- Concurrent mutation remains constrained by checkout and claim policy. Adding
  agents never makes shared filesystem writes safe.
- Web remains the primary rich interface. CLI retains full automation and
  administration; the TUI remains an operational fallback; Herdr remains the
  runtime host; MCP remains the structured agent coordination surface.

## Non-goals

- Model a company, payroll, human HR, or a fixed title taxonomy.
- Make the hierarchy itself confer permission.
- Promote every provider-local helper into a permanent agent.
- Keep every provider process running continuously.
- Treat transcripts, raw terminal output, or private reasoning as canonical
  knowledge.
- Require every safe bounded delegation or worker report to become an owner
  decision.
- Equate a domain, workstream, repository, checkout, folder, task, or provider
  thread.

## Acceptance direction

M22 is not complete until a browser-only real-provider scenario can:

1. open one domain backed by several existing checkouts;
2. show a domain steward plus terrain, prefab, and asset-pipeline workstream leads;
3. resume and converse directly with two different durable Codex agents;
4. let a granted lead create a durable implementer, independent reviewer, and
   scenario tester without exceeding its staffing envelope;
5. route one prefab-format change to terrain and cooking agents through accepted
   domain knowledge and durable messages;
6. let the tester operate a bounded fixture/product surface and publish exact
   evidence while the reviewer independently reports a defect;
7. restart browser, daemon, Herdr, and one provider process without losing agent
   identity, hierarchy, owner conversation, or canonical coordination state;
8. explain every write conflict, dependency, blocker, and owner decision without
   requiring raw transcript reconstruction; and
9. remain usable at narrow and desktop widths with keyboard navigation, readable
   typography, visible focus, bounded lists, and no raw-terminal dependency.
