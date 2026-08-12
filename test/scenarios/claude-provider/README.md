# Claude provider and cross-provider handoff acceptance

This scenario uses recorded Codex and Claude Code executables. It proves the
Claude 2.x probe, strict one-shot MCP launch, fail-closed permission boundary,
structured completion, and a provider switch through Crewfold-owned durable mail.

The Codex fixture sends a handoff to the stopped Claude agent. A distinct Claude
run receives that message in its immutable briefing and completes a dependent
task. The scenario asserts that provider-private session identifiers do not cross
the boundary and that raw transcripts remain explicitly excluded from context.
It uses no credentials, network access, or model inference.
