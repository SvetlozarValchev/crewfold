# ADR-0002: Separate durable coordination from runtimes

- Status: accepted
- Date: 2026-08-12

## Context

Herdr already solves terminal layout, persistence, attachment, and observation for
many coding agents. Similar runtime capabilities may later come from direct
processes, tmux, remote nodes, or provider headless APIs. None of those systems
should become the only place tasks, messages, decisions, and ownership exist.

## Decision

Crewfold owns a provider-neutral durable domain model. Runtime drivers own process
and terminal lifecycle. Provider adapters translate agent-specific capabilities.

Herdr is the preferred first interactive runtime and should be reused rather than
forked or replicated. Its pane/agent state is an observation used in reconciliation,
not authority for task completion or knowledge.

## Consequences

- Users retain the real native terminal UI of each agent.
- Provider or runtime changes do not erase coordination history.
- Crewfold must reconcile state across process boundaries and tolerate partial
  failures.
- The first vertical slice needs both runtime and provider adapter interfaces.
- Some rich behavior degrades gracefully for generic terminal agents.
