# ADR-0004: MCP as the agent-facing coordination surface

- Status: accepted
- Date: 2026-08-12

## Context

Crewfold must support Codex, Claude Code, and other agents without coupling its
communication model to terminal screen scraping or one provider SDK. Agents need a
common way to read their task and briefing, use a mailbox, claim scope, report
progress, and submit results.

## Decision

Expose a run-scoped MCP server as the primary structured agent-facing surface.
Translate MCP calls into the same authorized domain commands used by human clients.
Use short-lived run identity and capability scope rather than trusting an agent ID
supplied as a tool argument.

Retain generic terminal delivery as a degraded fallback. MCP does not replace the
native local API, event protocol, runtime driver, or provider adapter.

## Consequences

- Providers with MCP support receive consistent coordination tools.
- Tool descriptions and payload schemas become a critical compatibility surface.
- Authentication must account for untrusted processes on the same machine.
- Agents without MCP can participate with reduced fidelity through wrappers,
  structured files, or terminal prompts.
- Provider-specific enhancements remain optional.
