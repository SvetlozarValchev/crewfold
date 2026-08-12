# Herdr runtime acceptance

This offline scenario runs the durable two-agent request/review/handoff flow
through the public `herdr` runtime and `fixture-terminal` provider surfaces. A
stateful recorded Herdr CLI endpoint hosts the real Crewfold pane supervisor and
fixture processes without requiring an installed terminal UI.

It proves schema gating, workspace/pane launch, scoped MCP, durable messaging,
runtime wake delivery, completion authority, and restart reconciliation. The
installed-Herdr conformance test remains separate and opt-in.
