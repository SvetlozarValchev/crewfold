# Codex provider acceptance

This scenario uses a recorded Codex CLI endpoint. It verifies capability and
authentication probing, the isolated launch manifest, the Crewfold STDIO MCP
bridge, provider lifecycle output, and accepted completion without network or
model usage.

The endpoint is deliberately provider-shaped rather than a fake core adapter:
the daemon registers the real `codex` adapter and launches its normal command.
Only the external Codex executable is replaced.
