# ADR-0010: Participant-bound cross-project collaboration

- Status: accepted
- Date: 2026-08-13

## Context

Project-scoped mail prevents an unrelated run from reading or waking for another
project's coordination. That default is correct, but it blocks a legitimate case:
an application agent needs a contract from a library agent, while each is assigned
to a different project and task. A workspace-wide chat would solve discovery by
discarding least authority. Binding only an agent is also insufficient because the
same durable agent may later run another task with different authority.

Cross-project collaboration must remain durable while either agent is offline,
survive daemon restart, preserve each message's originating project and task, and
continue to use the existing provider-neutral mailbox tools. It must not create a
dependency, claim, meeting, or knowledge fact merely because two agents talked.

## Decision

Crewfold adds an owner-created `participant_bound` thread kind. Creating one binds two
through eight distinct enabled agents to distinct exact active assigned tasks; the
initial set must span at least two projects. Each immutable binding freezes the
agent, task, task's project and assignment plus their human-readable names and
observed revisions. The thread has a monotonically increasing
participant revision, starting at one for the initial atomic roster. An owner may
invite one additional bound participant, up to eight, only with the exact expected
participant revision. A stale revision changes nothing.

Ordinary direct mail retains its existing project boundary. The participant thread
is a narrow exception: an authenticated run may list, read, acknowledge, or send
within that thread only when its run agent, project, and task exactly match one
active binding. Its selected recipient must also be a bound agent, and every send
still has exactly one recipient. There is no broadcast. Runs cannot create a
cross-project thread, invite a participant, alter a binding, or infer authority
from a similarly named project, checkout, repository, or dependency.

Messages retain the sender run's origin project and task. Durable queueing and
best-effort wake behavior are unchanged. A wrong-task run for the same agent is
not an authorized participant and cannot receive, transition, send into, or be
woken for the thread. Participant-bound messages accept no artifacts in this
first slice, even from the sender run; defining cross-project artifact authority
is deferred rather than inferred from thread membership. Direct mail retains its
existing sender-owned artifact behavior.

An owner may send into the thread without impersonating a participant, but cannot
name a project or task for that message; owner-origin participant mail is stored
unscoped. Supplying explicit owner scope is rejected rather than allowing origin
spoofing.

The existing MCP tools remain the complete agent surface:
`crewfold_send_message`, `crewfold_list_inbox`, `crewfold_read_message`, and
`crewfold_acknowledge_message`. Supplying an owner-created `thread_id` selects the
participant-bound exception. Context packet v3 is unchanged: its bounded inbox
summary may contain messages authorized by an exact participant binding, while
full bodies remain explicit MCP reads. Roster reads and live context deltas are
deferred to the later context-delta contract.

The owner socket adds explicit create, invite, and participant-list operations.
Thread/participant events and message origin fields make authority decisions
inspectable. Collaboration itself does not mutate project dependencies, work
claims, meetings, canonical knowledge, or repository state.

## Consequences

- Agents in adjacent, unrelated repositories can negotiate a contract without a
  shared worktree, provider session, transcript, or live overlap.
- Project isolation remains the default; cross-project access is an explicit,
  bounded, owner-authorized capability rather than workspace-wide ambient mail.
- Agent identity alone cannot carry collaboration authority into a later task.
- Adding a participant is concurrency-safe and auditable; stale invitations fail
  without partially extending visibility.
- Multi-recipient announcements, agent-managed rosters, roster MCP reads, live
  packet refresh, remote-user identity, and cross-machine collaboration remain
  separate capabilities.
