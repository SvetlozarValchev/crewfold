# Internal packages

Private Go packages live here, organized by capability rather than by provider:

```text
localapi/     implemented local API protocol and client
daemon/       implemented foreground daemon and socket lifecycle
cli/          implemented human/machine command surface
buildinfo/    implemented embedded build metadata
domain/       implemented storage/transport-neutral coordination and run records
store/        implemented SQLite migrations, projections, event journal, and run queue
execution/    implemented runtime/provider contracts, fake adapters, and direct supervision
mcp/          implemented run-scoped JSON-RPC/MCP protocol and fixture client
scheduler/    future expanded placement and dependency policy
supervisor/   conditions, recommendations, and policy responses
knowledge/    future canonical-knowledge curation and retrieval
runtime/      future interactive/remote runtime drivers and wider reconciliation
provider/     future concrete provider adapters
gitstate/     repository identity and checkout observations
policy/       authorization, approvals, and budgets
```

This list is a starting map, not a requirement to create empty abstraction layers.
Packages should appear when an end-to-end feature needs them.
