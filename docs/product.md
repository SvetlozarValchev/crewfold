# Product definition

## Primary user

The first user is a developer already running several terminal-based coding agents
across multiple repositories, branches, or worktrees. They are comfortable with
Git and command-line tools, but do not want to manually poll dozens of sessions or
copy context between them.

## Jobs to be done

### Start a coherent crew

Given an objective and existing repositories, define a small set of roles, assign
work, create or reuse checkouts, launch appropriate agent sessions, and see their
state from one place.

### Preserve continuity

Stop, restart, or switch a provider session without losing the task, its evidence,
the messages it received, or the handoff from the prior attempt.

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

## Roles in the first product

Roles are templates; a person can define others.

| Role | Responsibility | Default authority |
| --- | --- | --- |
| Owner | Sets objectives, policies, and approvals | Full local authority |
| Manager | Decomposes objectives and coordinates dependencies | Propose and coordinate |
| Implementer | Changes a scoped part of a project | Mutate assigned checkout |
| Reviewer | Inspects evidence and changes | Read plus structured findings |
| Context curator | Maintains concise shared knowledge | Propose revisions; limited auto-accept |
| CI watcher | Runs or observes checks and reports status | Read and execute allowlisted checks |
| Integrator | Consolidates compatible completed work | Local integration; push/merge gated |

No role name grants permission. Policy is explicit and attached to the agent,
task, project, and action.

## Core workflows

### 1. Register a project

The owner points Crewfold at a Git repository or checkout. Crewfold records its
canonical path, remote identity if present, default branch, checkouts, commands,
and project-specific policy. Registration is read-only unless initialization is
explicitly requested.

### 2. Define an agent

The owner names a durable agent, chooses a role and provider adapter, sets its
default project scope, and applies budgets. The definition exists even when no
provider process is running.

### 3. Assign and launch

The owner or manager creates a task with deliverables, dependencies, constraints,
and expected change surfaces. The scheduler selects an eligible agent and
checkout. Crewfold builds a context packet, creates a run, and asks a runtime
driver to launch or attach to the provider session.

### 4. Work and communicate

The running agent uses Crewfold tools to claim scope, read messages, report
progress, send a question, publish evidence, and request coordination. Crewfold
also observes Git and runtime state, but observations do not override explicit
task facts without reconciliation.

### 5. Handle overlap

If two tasks claim the same path, symbol, subsystem, or behavior, Crewfold records
an overlap. Policy may merely notify, request a manager decision, schedule a
meeting, or block one run before it writes further.

### 6. Complete and hand off

An agent proposes completion with evidence. Required checks or review run. Once
accepted, Crewfold records a handoff, releases claims, wakes dependents, and asks
the curator whether durable knowledge changed.

### 7. Resume after failure

A failed or stopped run leaves the task intact. A replacement run receives the
task, the latest accepted handoff/checkpoint, unresolved messages, relevant
evidence, and current repository state.

## Functional requirements

### Projects and checkouts

- Register multiple repositories and checkouts without relocating them.
- Recognize branches and Git worktrees by stable repository identity.
- Support shared read-only access to a checkout.
- Prefer one writable checkout per concurrent implementation task.
- Support an explicitly configured shared-writer mode with claims and warnings.

### Agents and runs

- Separate durable agent definitions from concrete process runs.
- Support at least Herdr, direct subprocess, and test/fake runtime drivers.
- Support a generic terminal provider plus enhanced provider adapters.
- Track lifecycle as observed, claimed, and reconciled state.
- Bound concurrency globally and per provider/project.

### Tasks and scheduling

- Express parent/child tasks and dependency edges.
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
- Scope by workspace, project, component, task, and role.
- Provenance, authority, confidence, freshness, and supersession.
- Full-text retrieval first; optional semantic retrieval later.
- Curator proposals that can be accepted by rule or human review.

### Supervision

- Detect blocked, idle, failed, stale, and over-budget runs.
- Detect planned and observed work overlap.
- Recommend retry, reassignment, review, meeting, or escalation.
- Never exceed the action policy attached to the initiating identity.

### Auditability

- Record who or what performed each coordination mutation.
- Link summaries and decisions to evidence.
- Explain why an agent received each item in a context packet.
- Permit export without provider-private session internals.

## Quality attributes

| Attribute | Initial target |
| --- | --- |
| Offline operation | Core coordination works with no network except provider calls |
| Startup | Local daemon becomes responsive in under two seconds on a warm machine |
| Scale | 100 definitions and 100,000 events without operational administration |
| Recovery | Process restart does not lose committed coordination state |
| Compatibility | Unknown providers can use generic terminal plus MCP mode |
| Observability | Every scheduler and supervisor action has a human-readable reason |
| Safety | External/shared mutations require explicit policy and usually approval |
| Portability | Linux first; macOS next; Windows through WSL before native support |

## First-release acceptance scenario

The first meaningful release passes this scenario:

1. Register three checkouts of one repository.
2. Define an implementer, reviewer, and CI watcher using at least two different
   provider adapters.
3. Give the implementer a task and launch it through Herdr.
4. Send it a durable message from the owner and receive an acknowledgement.
5. Launch a reviewer whose context contains the task and the implementer's handoff
   but not the entire implementer transcript.
6. Detect a deliberately overlapping claim and create a two-agent coordination
   thread or meeting.
7. Stop one run and resume the same durable agent and task in a new session.
8. Show the owner a coherent status view and auditable event trail.

## Explicitly deferred

- Multi-user accounts and organization tenancy.
- Hosted control plane or synchronization service.
- GitHub App installation and remote CI mutation.
- Automatic cross-machine workload placement.
- Model routing marketplaces and centralized billing.
- Autonomous production deployment.
- A general DAG/workflow authoring product.
- A proprietary Crewfold-only agent protocol.
