# Vision

## One sentence

Crewfold lets one developer govern more heterogeneous coding-agent work than they
can personally inspect while retaining evidence-backed understanding and control.

## The immediate product

The immediate product is not an autonomous company. It is a dependable local
manager for one person's agents.

A developer may have `world-engine`, `world-engine-2`, and `world-engine-3` checked
out as separate branches or worktrees. One agent implements a system, another
reviews a neighboring subsystem, and a third watches tests or CI. They need more
than independent terminals:

- a shared statement of the project's purpose and constraints;
- explicit tasks and ownership;
- awareness of overlapping plans and actual file changes;
- durable messages and handoffs;
- a way to convene a two- or three-agent review;
- a supervisor that knows dependencies and escalation rules;
- continuity when any individual session is restarted.

Crewfold supplies that missing layer.

## The control problem

As agent autonomy increases, code review by personal comprehension stops scaling.
Five or ten agents can produce tens or hundreds of thousands of lines while the
owner is still deciding whether to continue, redirect, consolidate, or stop. A
terminal grid and a collection of session summaries do not solve that problem;
they replace code overload with prose overload.

Crewfold is therefore a management-compression system as well as a coordination
control plane. It converts a large volume of agent activity into a smaller set of
governed facts:

- intended and accepted deliverables;
- material decisions, assumptions, and rationale;
- verification evidence and its freshness and independence;
- deviations, unresolved risks, and explicit unknowns;
- cross-task effects, duplicated work, and contradictions;
- decisions and interventions that require the owner.

The owner should reason at task, objective, project, and portfolio level. Source,
diffs, individual runs, and provider transcripts remain available for drill-down,
but reconstructing them is never the normal path to understanding current state.

## North-star experience

The owner opens one console and sees:

- what every project is trying to achieve;
- what was actually delivered since the last owner checkpoint;
- why material implementation and architecture decisions were made;
- which delivery claims are accepted, disputed, stale, or weakly evidenced;
- the current reliability/stability assessment and the evidence behind it;
- residual risks, unknowns, and incomplete promises;
- which roles exist and which sessions are actually running;
- what each agent owns, is waiting for, or needs from the human;
- where work overlaps or contradicts a recorded decision;
- the minimum useful summary of recent progress;
- recommended actions such as review, consolidation, retry, or escalation.

The owner can ask a manager agent to decompose an objective. The manager proposes
tasks, roles, dependencies, and budgets. Once approved, Crewfold launches the
needed sessions, supplies scoped context, and mediates their coordination. The
owner can inspect or interrupt every step.

The same information is queryable through stable CLI/API records. The console is
one presentation of an outcome projection, not the only place where a model has
summarized the crew.

## What “shared context” means

Shared context is not a shared transcript. It consists of governed artifacts:

- project briefs and current objectives;
- constraints and policies;
- accepted architectural decisions;
- active tasks, claims, dependencies, and risks;
- concise handoffs and validated findings;
- unresolved questions and conflicts;
- pointers to source files, commits, tests, and external evidence.

Every item has provenance, scope, freshness, authority, and revision history. A
context curator converts noisy observations into proposed durable knowledge. Rules
or a human decide what becomes canonical.

## Scale target

Crewfold's local architecture should remain responsive with approximately:

- 100 registered durable agent definitions;
- 100,000 coordination events;
- 1,000 active or completed tasks;
- 10–20 concurrent local runs on a sufficiently capable machine;
- multiple repositories and many worktrees;
- years of compacted decisions and handoffs.

This is a design target, not a promise that one workstation can afford or safely
execute one hundred simultaneous model sessions. Registration, scheduling, and
concurrency are distinct concerns.

## Principles

### The crew survives its sessions

An agent identity is a durable role. A run is one attempt by one concrete provider
session. Stopping a terminal must not erase ownership, communication, or context.

### Coordination is structured

Important outcomes become tasks, decisions, claims, messages, or knowledge items.
Free-form dialogue can support those records but does not replace them.

### Activity is not delivery

Agent reports, Git changes, passing checks, reviewer findings, and accepted
deliverables are distinct facts. Crewfold never equates tokens spent, files
changed, commits produced, or an agent saying “done” with achieved outcome.

### Understanding is progressively summarized

Run facts roll into task outcomes, task outcomes into objectives, and objectives
into project/portfolio briefings. Each level preserves provenance and exposes
exceptions; it does not concatenate lower-level prose. A concise answer must be
drillable to the decisions and evidence that justify it.

### The source of truth stays narrow

Crewfold does not duplicate source files or Git history. It stores coordination
facts and references evidence where it naturally lives.

### Autonomy is a budget, not a personality

Each role receives explicit authority, time, token/cost, concurrency, filesystem,
and network limits. A “boss” is a policy-constrained supervisor, not an
unrestricted superuser prompt.

### Replaceable edges, stable center

Terminal multiplexers and coding agents will change. The durable domain model and
protocol must outlive any single integration.

## Non-goals for the personal product

- Replacing Herdr, tmux, IDEs, Git, or model-provider interfaces.
- Building a new general-purpose coding agent.
- Giving models silent authority to merge, push, deploy, or contact people.
- Keeping an exhaustive, globally visible transcript of every action.
- Solving organizational identity, billing, compliance, and cross-company trust.
- Guaranteeing conflict-free concurrent writes to one checkout.
- Inventing a visual workflow language before basic task delegation works.
- Requiring embeddings, a vector database, Kubernetes, or a cloud account.

## Growth into an organization product

The later organization-wide product is compatible with this scope if Crewfold
keeps its boundaries:

| Personal now | Organization later |
| --- | --- |
| One local owner | Authenticated users, teams, and service identities |
| Local Unix socket | Secure remote API and synchronization |
| SQLite | Server database plus local replicas/caches |
| Local policy file | Organization policy and delegated administration |
| Local agent directory | Federated agent registry and ownership |
| One-machine scheduler | Distributed placement and quotas |
| Local audit trail | Retained organization audit and compliance controls |

The future system can add these implementations behind versioned contracts. It
must not require the first release to simulate an enterprise prematurely.
