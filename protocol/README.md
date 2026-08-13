# Protocol

This directory holds the single current JSON Schemas, generated-type
configuration, conformance fixtures, and strict contract tests for:

- local daemon commands, queries, and event streams;
- domain event envelopes and payloads;
- runtime-driver and provider-adapter messages;
- exported records and context packets.

MCP tool schemas are generated or validated against the same domain payloads but
remain a separate agent-facing surface.

Published schemas currently cover version/self/runtime-doctor/provider-doctor/error responses; the
newline-delimited local request/response envelope; daemon and database status;
durable workspace/event records; project, repository, and checkout observation;
and provider-neutral agent, objective, task, dependency, assignment, readiness,
budget, run, placement, timeline, handoff, fake/direct fixture scenario, bounded
run-log, run-stop, coordination-status, claim/overlap/drift, structured meeting,
canonical knowledge/retrieval/curator/contradiction, and portable project-knowledge
records. Later schemas remain proposals. See the
[event catalogue](../docs/reference/events.md) and [MCP tool
contract](../docs/reference/mcp-tools.md).
