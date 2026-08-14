# Open questions

These decisions are intentionally unresolved. An implementation should not settle
them accidentally through an incidental library or schema choice.

## Decisions still required before public release

### License

Current proposal: **Apache-2.0**.

Why it fits: permissive use, explicit patent terms, broad adapter and company
adoption, and compatibility with a possible hosted service. The owner must approve
the choice before a `LICENSE` file is added. Until then, the repository grants no
open-source license.

## Decisions already closed

The local transport, SQLite/query boundary, and TUI library are no longer open:
the current contracts use newline-delimited JSON over an owner-only Unix socket,
the vendored CGO-free `github.com/ncruces/go-sqlite3` plus sqlc and one exact
baseline, and Bubble Tea v2/Bubbles v2/Lip Gloss v2 for the Go-native dashboard.
M20 backup uses SQLite's online backup API and has no alternate driver or legacy
schema/bundle path.

## Decisions still required for later product shape

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

The current Herdr adapter uses its structured CLI, installed-schema probe, stable
terminal identity, and recorded fixtures. A later long-lived subscription is open
only if Herdr exposes a stable negotiated local API; M20 recovery does not preserve
or resume a Herdr session.

### Provider adapter packaging

Current first-party Codex and Claude adapters compile into the binary behind the
same runtime/provider contracts. A third-party plugin/conformance boundary remains
open for M21; installation alone must not confer daemon-level authority.

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
- Which future supervisor signals should recommend the already-explicit owner
  context refresh, without turning it into background push or provider steering?
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
