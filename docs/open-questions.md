# Open questions

These decisions are intentionally unresolved. An implementation should not settle
them accidentally through an incidental library or schema choice.

## Decisions required before implementation

### License

Current proposal: **Apache-2.0**.

Why it fits: permissive use, explicit patent terms, broad adapter and company
adoption, and compatibility with a possible hosted service. The owner must approve
the choice before a `LICENSE` file is added. Until then, the repository grants no
open-source license.

### Exact local API framing

Candidates:

- newline-delimited request/response and event messages over a Unix socket;
- HTTP semantics over a Unix socket;
- Connect/gRPC with generated clients.

Recommendation for the spike: prototype plain versioned JSON messages and measure
subscription, cancellation, and SDK ergonomics before committing.

### Go SQLite driver and query generation

Choose after testing:

- pure-Go versus CGO distribution constraints;
- FTS5 availability;
- backup API support;
- cancellation and busy behavior;
- generated query ergonomics and migration testing.

### Terminal TUI library

Bubble Tea is the leading option. Validate large-list performance, mouse behavior,
and interaction with direct Herdr attach before treating it as final.

## Decisions required during the first vertical slice

### Daemon lifecycle

Should the CLI auto-start a per-user daemon, require an explicit daemon command,
or support both with one documented authority? How should a user service be
installed without surprising process persistence?

### Agent authentication to local MCP

Implemented for the trusted fixture: one run-scoped, short-lived HMAC bearer
capability is delivered through an owner-only file path and bound to one immutable
context packet. It is absent from process arguments, environment values, SQLite,
and committed config. The remaining decision is the containment/owner-API
authentication boundary required before untrusted same-UID provider processes are
supported.

### Runtime event ingestion

Use Herdr's local socket directly for long-lived subscriptions or begin with its
structured CLI. Prefer the socket when it offers stable version negotiation, but
keep CLI fixtures for testing and diagnosis.

### Provider adapter packaging

Decide whether first-party adapters compile into the binary, run as supervised
plugins, or use both tiers. Third-party adapters should not receive daemon-level
authority merely because they are installed.

### Project-local configuration

Define which configuration is safe and useful to commit in `.crewfold/`, and which
always remains under the owner's configuration/state directories. Project files
must never contain model credentials or machine-specific socket capabilities.

## Product questions to validate with use

- Does the human primarily operate from Crewfold's TUI or from a manager-agent
  conversation inside Herdr? Both must consume the same structured outcome and
  briefing projection rather than develop separate versions of project truth.
- Which message kinds genuinely help, and which create bureaucracy?
- When does a meeting produce better outcomes than a simple reviewer handoff?
- Are path claims sufficient for most overlap, or is symbol/component indexing
  needed early?
- How frequently should context refresh without distracting active agents?
- Which knowledge updates can safely auto-accept?
- What is a realistic default concurrent-run limit by provider and machine?
- Should a durable agent normally keep one provider identity, or freely switch
  providers between runs?
- How much provider-native session resumption is useful compared with a clean run
  from a strong handoff?

## Explicitly postponed organization questions

- User/team identity and authentication.
- Who owns an agent created by a coworker?
- Visibility boundaries across projects and people.
- Cross-machine message delivery and offline conflict resolution.
- Central versus federated scheduling.
- Organization policy inheritance and delegated administration.
- Cost allocation, quotas, audit retention, and compliance.
- Consolidation authority when two real people own overlapping work.

The personal data model should not preclude these, but no current milestone needs
to answer them.
