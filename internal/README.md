# Internal packages

Private Go packages live here, organized by capability rather than by provider:

```text
localapi/     implemented local API protocol and client
daemon/       implemented foreground daemon and socket lifecycle
cli/          implemented human/machine command surface
buildinfo/    implemented embedded build metadata
api/          future domain API and MCP translation
domain/       commands, invariants, and core types
store/        SQLite migrations, queries, event journal, and queues
scheduler/    deterministic placement and dependencies
supervisor/   conditions, recommendations, and policy responses
knowledge/    curation, retrieval, and context packets
runtime/      runtime-driver contracts and reconciliation
provider/     provider-adapter contracts
gitstate/     repository identity and checkout observations
policy/       authorization, approvals, and budgets
```

This list is a starting map, not a requirement to create empty abstraction layers.
Packages should appear when an end-to-end feature needs them.
