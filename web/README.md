# Web console — deferred

A local browser console may eventually visualize large crews, task dependencies,
messages, meetings, claims, knowledge revisions, and audit history.

It is deliberately not part of the first implementation. The CLI/TUI, Herdr, and
agent MCP surface must prove the control-plane model first. When built, the console
will consume the same versioned API and will not become a second source of truth.
