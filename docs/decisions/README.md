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
