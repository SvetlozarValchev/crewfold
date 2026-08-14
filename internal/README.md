# Internal packages

Private Go packages live here, organized by capability rather than by provider:

```text
localapi/     implemented local API protocol and client
daemon/       implemented foreground daemon and socket lifecycle
cli/          implemented human/machine command surface
buildinfo/    implemented embedded build metadata
domain/       implemented storage/transport-neutral coordination and run records
store/        one exact SQLite baseline, projections, journals, durable coordination, canonical integrity, knowledge, and portable import/export
execution/    implemented runtime/provider contracts, fake/direct/Herdr supervision, and fixture providers
herdr/        implemented installed-schema probe and structured Herdr CLI client
mcp/          implemented run-scoped JSON-RPC/MCP protocol and fixture client
gitstate/     repository identity and checkout observations
```

This list is a starting map, not a requirement to create empty abstraction layers.
Packages should appear when an end-to-end feature needs them.
