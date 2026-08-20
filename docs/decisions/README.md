# Architecture decision records

ADRs capture consequential decisions whose context and tradeoffs would otherwise
be lost. They are append-only historical records: when a decision changes, mark the
old ADR superseded and add a new one.

Use four sections:

```text
# ADR-NNNN: Title

- Status: proposed | accepted | superseded | rejected
- Date: YYYY-MM-DD

## Context
## Decision
## Consequences
```

Not every implementation detail needs an ADR. Use one for decisions that change
product boundaries, persisted data, public protocols, security authority,
deployment shape, or difficult-to-reverse dependencies.

Current decisions:

- [ADR-0023: Operable workstreams and managed local services](0023-operable-workstreams-and-managed-local-services.md)
- [ADR-0022: Workstream execution homes and durable context continuity](0022-workstream-execution-and-context-continuity.md)
- [ADR-0021: Domain-oriented durable-agent console](0021-domain-oriented-durable-agent-console.md)
- [ADR-0020: Local web workbench as the primary owner interface](0020-local-web-workbench.md)
- [ADR-0019: Personal-scale hardening and quiescent recovery](0019-personal-scale-hardening-and-recovery.md)
- [ADR-0018: Go-native operator TUI over the canonical local API](0018-go-native-operator-tui.md)
- [ADR-0017: Owner-reviewed outcomes and bounded management briefings](0017-owner-reviewed-outcomes-and-bounded-briefings.md)
- [ADR-0016: Owner-granted local checks as fresh mechanical evidence](0016-owner-granted-local-check-evidence.md)
- [ADR-0015: Owner-granted manager proposals and deterministic supervision](0015-owner-granted-manager-proposals-and-deterministic-supervision.md)
- [ADR-0014: Explicit bounded live context deltas](0014-explicit-bounded-live-context-deltas.md)
- [ADR-0013: Portable project knowledge snapshots](0013-portable-project-knowledge-snapshots.md)
