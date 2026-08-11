# Protocol

This directory will hold versioned JSON Schemas, generated-type configuration,
conformance fixtures, and compatibility tests for:

- local daemon commands, queries, and event streams;
- domain event envelopes and payloads;
- runtime-driver and provider-adapter messages;
- exported records and context packets.

MCP tool schemas are generated or validated against the same domain payloads but
remain a separate agent-facing surface.

M0 publishes version, self-doctor, and error response schemas under
`schemas/cli/v1/`. M1 publishes the newline-delimited local request/response,
hello, status, and stop schemas under `schemas/local/v1/`. Later schemas remain
proposals. See the
[event catalogue](../docs/reference/events.md) and [MCP tool
contract](../docs/reference/mcp-tools.md).
