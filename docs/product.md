# Product definition

## Primary user

The first user is a developer already running several terminal-based coding agents
across multiple repositories, branches, or worktrees. They are comfortable with
Git and command-line tools, but do not want to manually poll dozens of sessions or
copy context between them.

## Jobs to be done

### Organize a domain, not a directory

Group related engineering work under one durable knowledge and coordination
boundary even when it spans several repositories, worktrees, services, and
independent objectives. A checkout is an execution resource, not an organizational
container.

### Direct durable agents

Open any named agent's real resumable provider conversation, understand its
objective and children, give it direction directly, and preserve that continuity
when its current process stops. Let granted agents create bounded durable children
for continuing implementation, review, and testing work.

### Start a coherent crew

Given an objective and existing repositories, define a small set of roles, assign
work, create or reuse checkouts, launch appropriate agent sessions, and see their
state from one place.

### Preserve continuity

Stop, restart, or switch a provider session without losing the task, its evidence,
the messages it received, or the handoff from the prior attempt.

### Understand delivery without inspecting every implementation

Given more agent-produced code than the owner can personally read, explain what
was promised, what was actually accepted, why material decisions were made, how
strong and fresh the verification is, what changed for compatibility or
stability, and what remains risky or unknown. The explanation must come from
durable, provenance-linked records rather than polling sessions or reconciling
agent-written Markdown.

### Coordinate overlapping work

Detect when planned or actual change surfaces overlap. Make the conflict visible,
pause unsafe successors when policy requires it, and convene the right agents to
produce one resolution.

### Share only useful context

Give each agent a compact briefing containing current goals, constraints,
decisions, dependencies, relevant knowledge, and messages—within a declared
budget and without dumping every historical transcript.

### Supervise by exception

Let normal work continue without constant attention. Notify the owner when an
agent is blocked, a dependency changes, a run exceeds budget, a claim conflicts,
or an external side effect needs approval.

### Decide what happens next

At an owner checkpoint, show what changed since the prior checkpoint and the few
choices that require attention. The owner should be able to continue, review,
redirect, consolidate, retry, pause, or stop work from recommendations that cite
their supporting facts and expose uncertainty.

## Owner-defined work patterns in the first product

Agent role strings are arbitrary descriptive labels, not an enum or authority
taxonomy. The following are illustrative work patterns only; explicit grants,
exact agent-bound launch profiles, packet capabilities, and owner policy confer
authority.

| Example pattern | Responsibility | Possible explicit capability |
| --- | --- | --- |
| Owner | Sets objectives, policies, and approvals | Full local authority |
| Coordinator | Decomposes objectives and coordinates dependencies | Exact bounded proposal grant |
| Change producer | Changes a scoped part of a project | Assigned-checkout mutation |
| Evidence reviewer | Inspects evidence and changes | Exact review/report capability |
| Context curator | Maintains concise shared knowledge | Propose revisions; bounded configured derivation |
| Check observer | Runs or observes checks and reports status | Exact allowlisted check-watch grant |
| Integrator | Consolidates compatible completed work | Exact local integration grant; push/merge gated |

No role name grants permission. Policy is explicit and attached to the agent,
task, project, and action.

## Domain-oriented product mapping

The owner interface presents the current canonical scopes this way:

| Owner concept | Initial canonical backing | Meaning |
| --- | --- | --- |
| Workspace | Workspace | One local owner's portfolio and defaults |
| Domain | Project | Shared knowledge, policy, workstreams, resources, and agents; never one folder |
| Workstream | Objective plus one optional primary checkout | One independently managed outcome with a persistent execution home when it changes source |
| Attached resource | Repository, checkout, service | A place or process used by work; never the hierarchy |
| Durable agent | Agent definition plus resumable provider binding | A named continuing actor that can be idle or running |
| Agent attempt | Run | One provider/runtime execution of that durable agent |

A domain may have several peer orchestrators and arbitrarily deep durable agent
relationships. One preferred agent may be the default owner entry point, but
there is no one-executive-per-domain or one-agent-per-checkout rule. Hierarchy
organizes attention and roll-up; explicit grants, profiles, policies, assignments,
claims, and budgets confer authority.

The owner UI may offer editable starting templates for familiar responsibilities
such as domain coordination, workstream coordination, implementation,
independent review, verification, knowledge maintenance, and integration. These
templates only prefill an ownership brief. They are not role enums, do not choose
authority, and never prevent the owner from writing a custom responsibility.

## Core workflows

### 1. Register a domain and its resources

The owner creates or selects a domain, then attaches one or more Git repositories,
checkouts, services, or other bounded resources. Crewfold records canonical
identity, branch/HEAD observations, commands, and domain policy. Registration is
read-only unless initialization is explicitly requested.

### 2. Define or delegate a durable agent

The owner names a durable agent, chooses a descriptive role and provider adapter,
reviews its durable operating charter and direct-work/delegation policy, places it
in the domain attention tree, sets its eligible workstreams/resources, and applies
budgets. A short-lived read-only Codex helper may draft the name, role, charter,
and policy from owner intent, but the draft records no state and grants no
authority until the owner reviews and creates the definition. The definition and
resumable conversation identity exist even when no provider process is running.
A selected starting template remains ordinary editable owner input throughout
this flow; the reviewed charter, not the template label, is what becomes durable.
A manager with a bounded staffing grant may create a durable child inside that
exact envelope; the child's charter describes expected behavior while the grant
still supplies the only creation authority.

### 3. Assign and launch

The owner creates work directly or accepts an exact bounded proposal containing
deliverables, dependencies, required predecessor outputs, constraints, intended
agent placement, and expected change surfaces. An implementation workstream binds
one existing writable checkout as its primary execution home. Proposal acceptance
atomically binds that checkout, places every participating durable agent, creates
the graph, and publishes scheduling intents. The scheduler validates its
owner-authored agent-bound launch profile and reuses that exact checkout. Crewfold
builds a context packet, creates a run, and asks a runtime driver to launch the
provider process without cloning, relocating, cleaning, installing, or rebuilding
the checkout implicitly.

### 4. Work and communicate

The running agent uses Crewfold tools to claim scope, read messages, report
progress, send a question, publish evidence, and request coordination. Crewfold
also observes Git and runtime state, but observations do not override explicit
task facts without reconciliation.

The owner can open and speak to any durable agent's real provider conversation.
Agent-to-agent facts still travel through durable messages and governed knowledge
rather than relying on one provider transcript being visible to another.

### 5. Handle overlap

If two tasks claim the same path, symbol, subsystem, or behavior, Crewfold records
an overlap. Policy may merely notify, request a manager decision, schedule a
meeting, or block one run before it writes further.

### 6. Complete and hand off

An agent proposes completion with evidence. Required checks or review run. Once
accepted, Crewfold records a handoff, releases claims, and wakes only dependents
whose declared predecessor-output requirements are satisfied. Review,
remediation, and verification successors receive the bounded completion summary,
handoff, findings/risks, checks, changed paths, and authorized evidence references
they require; a bare `completed` status is not a substitute. Crewfold then asks
the curator whether durable knowledge changed.

### 7. Resume after failure

A failed or stopped run leaves the task intact. A replacement run receives the
task, the latest accepted handoff/checkpoint, unresolved messages, relevant
evidence, and current repository state.

### 8. Review outcomes and continue

The owner first records an immutable deliverable commitment for the exact task,
then proposes and explicitly accepts or rejects its structured assessment.
Crewfold derives an owner briefing across tasks, objectives, projects, and the
workspace.
It separates attempted activity from accepted delivery; connects decisions and
rationale to outcomes; grades verification strength and freshness; surfaces
duplicates, contradictions, risks, and unknowns; and identifies decisions that
need the owner. The owner can approve the next work, intervene, or drill into the
supporting records without reading each session.

### 9. Operate from one domain console

The owner opens the web console and navigates domains and expandable durable-agent
trees. Selecting an agent opens its real resumable conversation and structured
activity, with objective, resources, children, changes, checks, evidence,
messages, and decisions alongside it. Selecting a domain opens shared knowledge,
workstreams, interfaces, upstream impact, documents, services, and owner
attention. Raw terminal attachment and the TUI remain advanced operational
fallbacks. Every summary drills into the exact canonical records that produced it.

## Functional requirements

### Projects and checkouts

- Register multiple repositories and checkouts without relocating them.
- Recognize branches and Git worktrees by stable repository identity.
- Support shared read-only access to a checkout.
- Bind every source-mutating workstream to one owner-selected, persistent,
  writable primary checkout; coordination-only workstreams may omit one.
- Let domain-level agents inspect all attached checkouts through bounded
  read-only authority while workstream agents default to their workstream home.
- Reuse warm checkout state across provider processes and task attempts; never
  imply a clone, dependency install, clean, or bootstrap from process launch.
- Prefer one writable checkout per concurrent implementation workstream.
- Support an explicitly configured shared-writer mode with claims and warnings.
- Show an explicit warning when multiple active workstreams share one checkout.

### Agents and runs

- Separate durable agent definitions from concrete process runs.
- Preserve a resumable provider conversation per durable agent when supported;
  replacement runs recover from canonical context when it is not.
- Support an acyclic owner-visible manager/child relationship without deriving
  authority from that relationship.
- Permit bounded child-agent creation only through an exact staffing grant and
  record every created identity and launch receipt.
- Support at least Herdr, direct subprocess, and test/fake runtime drivers.
- Support a generic terminal provider plus enhanced provider adapters.
- Track lifecycle as observed, claimed, and reconciled state.
- Bound concurrency globally and per provider/project.
- Present the durable conversation and all attached task attempts as one ordered
  agent timeline and derive status from the most consequential current activity.

### Tasks and scheduling

- Express parent/child tasks and dependency edges.
- Declare whether each dependency requires completion only, a handoff, or a
  handoff plus referenced evidence before the successor can launch.
- Record deliverables, constraints, priority, budgets, and change-surface hints.
- Use leases so abandoned assignments can be recovered.
- Avoid scheduling tasks whose dependencies or required claims are unavailable.
- Keep all automatic scheduling decisions explainable.

### Communication

- Durable direct messages, group threads, announcements, and acknowledgements.
- Structured requests for information, review, approval, and handoff.
- Two- and multi-agent meetings with an agenda, facilitator, timebox, source
  context, participant contributions, resolution, and resulting actions.
- Inbox summaries in every context packet.

### Knowledge

- Versioned project briefs, decisions, constraints, glossary entries, findings,
  and runbooks.
- Keep domain knowledge independent of checkout paths and route relevant changes
  across affected workstreams without copying whole transcripts.
- Scope by workspace, project, component, task, and role.
- Provenance, authority, confidence, freshness, and supersession.
- Full-text retrieval first; optional semantic retrieval later.
- Curator proposals that can be accepted by rule or human review.

### Supervision

- Detect blocked, idle, failed, stale, and over-budget runs.
- Detect planned and observed work overlap.
- Recommend retry, reassignment, review, meeting, or escalation.
- Never exceed the action policy attached to the initiating identity.

### Outcomes and management understanding

- Record deliverable commitments and acceptance independently from agent activity.
- Require the local owner to create each immutable task-scoped commitment before
  its assessment; agents and run-scoped tools have no outcome authority.
- Give outcome assessments a proposed, accepted, rejected, or superseded review
  state and an achieved, partial, not-achieved, or unknown conclusion.
- Link outcomes to evidence, decisions, compatibility effects, risks, unknowns,
  and follow-up work.
- Preserve material rationale and constraints without storing hidden model
  reasoning or requiring full transcripts.
- Let assessment input cite only exact handoffs or check-requirement evidence;
  derive self-report, mechanical check, exact independent-review provenance, and
  accepted-governance verification rather than accepting caller labels.
- Maintain revisioned projections at run, task, objective, project, and workspace
  scope.
- Produce a bounded “since checkpoint” briefing of delivered changes, deviations,
  failed verification, active risk, unknowns, and required owner actions.
- Capture the current workspace event high-water for each briefing and permit
  only an exact same-scope checkpoint as its exclusive lower bound.
- Make every aggregate claim explainable through durable source records.
- Keep stale, disputed, and contradictory evidence visible rather than blending
  it into a confident narrative.
- Treat code, diffs, logs, and transcripts as optional drill-down evidence, not as
  the primary management interface.

### Auditability

- Record who or what performed each coordination mutation.
- Link summaries and decisions to evidence.
- Explain why an agent received each item in a context packet.
- Permit export without provider-private session internals.

### Operator interface

- Provide one owner-local domain-oriented web console embedded in the existing
  binary, with no production Node process or separate frontend service.
- Make the selected durable agent's real provider conversation the primary work
  surface; make the domain overview the primary cross-workstream surface.
- Keep the Go-native terminal dashboard as a keyboard-complete SSH/operational
  fallback rather than a competing management interface.
- Consume canonical local-API records and M18 briefing claims unchanged. Event
  envelopes invalidate records but never become an alternate fact projection.
- Preserve stable-ID selection and visibly stale cached state across reconnect;
  disable interventions until canonical synchronization succeeds.
- Render valid bounded UTF-8 after removing terminal controls and bidirectional
  formatting controls from every external string.
- Preserve state, severity, focus, and selection as text under `NO_COLOR`.
- Submit mutations only after an exact review and through the normal typed,
  expected-revision, idempotent API.
- Attach through the exact shell-free `RunAttach` argv and never display attach
  environment values or opaque runtime/provider handles.

## Quality attributes

| Attribute | Initial target |
| --- | --- |
| Offline operation | Core coordination works with no network except provider calls |
| Startup | Local daemon becomes responsive in under two seconds on a warm machine |
| Scale | 100 definitions and 100,000 events without operational administration |
| Recovery | Process restart does not lose committed coordination state |
| Compatibility | Unknown providers can use generic terminal plus MCP mode |
| Observability | Every scheduler and supervisor action has a human-readable reason |
| Comprehension | Owner briefings explain accepted outcomes, rationale, evidence, risk, unknowns, and required decisions with provenance |
| Safety | External/shared mutations require explicit policy and usually approval |
| Portability | Linux first; macOS next; Windows through WSL before native support |

## First-release acceptance scenario

The first meaningful release passes this scenario:

1. Register three checkouts of one repository.
2. Define three arbitrarily named agents for change, evidence review, and check
   observation, using at least two different provider adapters; attach explicit
   capabilities independently of their role strings.
3. Give the change-producing agent a task and launch it through Herdr.
4. Send it a durable message from the owner and receive an acknowledgement.
5. Launch the evidence agent with an exact review assignment whose context contains
   the task and handoff but not the entire prior transcript.
6. Detect a deliberately overlapping claim and create a two-agent coordination
   thread or meeting.
7. Stop one run and resume the same durable agent and task in a new session.
8. Give exactly one arbitrarily named agent a current-packet check-watch grant, run a
   clean-HEAD named check through fail/repair/pass, and route the failure to the
   exact task owner and evidence duty without reading any role/purpose string.
   A daemon restart reconciles one child/result or explicit unknown, and a later
   HEAD/dirty observation makes old evidence visibly stale.
9. Prove that check evidence changes no task-completion, policy-acceptance,
   commit/push/merge/deploy, or integration-order state.
10. Generate enough concurrent activity that reading every transcript is not a
   viable way to understand the project.
11. Show a bounded project briefing that separates attempted work from accepted
   outcomes and captures material decisions and compatibility effects.
12. Identify missing, stale, self-reported, and independently verified evidence;
    never describe weakly supported work as reliable.
13. Surface duplicated or contradictory work, unresolved risk, and the available
    consolidation or escalation path.
14. Answer what changed, why, how much to trust it, what remains, and what needs
    the owner without requiring session transcripts.
15. Drill every material briefing claim into durable records and its auditable
    event trail.

## Explicitly deferred

- Multi-user accounts and organization tenancy.
- Hosted control plane or synchronization service.
- GitHub App installation and remote CI mutation.
- Automatic cross-machine workload placement.
- Model routing marketplaces and centralized billing.
- Autonomous production deployment.
- A general DAG/workflow authoring product.
- A proprietary Crewfold-only agent protocol.
