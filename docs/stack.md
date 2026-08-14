# Technology stack

## Recommendation

Build the local core as a single Go binary backed by SQLite. Expose a versioned
local API and MCP server, and integrate replaceable direct and Herdr runtimes. The
control plane now works end to end through M20; M21 adds the previously deferred
owner-local web workbench without creating a second production service.

The current implementation includes the Go binary, local Unix transport, SQLite
storage, read-only installed-Git observation, and provider-neutral durable work
coordination. It also includes a run-scoped MCP facade and immutable base context
packets for the trusted fixture provider. M21 now also includes pinned and embedded
React/Vite production assets plus the authenticated loopback shell/status boundary;
onboarding, SSE invalidation, terminal WebSockets, and command surfaces remain
planned. Later rows remain the proposed baseline until their capability is implemented.

## Core

| Area | Choice | Reason |
| --- | --- | --- |
| Daemon and CLI | Go, current supported stable release | Single binary, strong process/concurrency support, low idle cost |
| Local database | SQLite in WAL mode through `github.com/ncruces/go-sqlite3` | Transactional, CGO-free, portable, and operationally trivial at personal scale |
| SQL access | Pinned `sqlc` generated Go over explicit SQL | Compile-time query types while schema and transaction behavior stay visible |
| Database schema | One embedded current baseline | Reproducible initialization and simple backups |
| Local transport | JSON messages over a Unix domain socket | Inspectable, stream-capable, user-local by default |
| Schema | Embedded JSON Schema validated by `github.com/santhosh-tekuri/jsonschema/v6`, plus generated language types | One executable current contract catches omitted, null, malformed, and out-of-scope wire data before Go zero values can erase those distinctions |
| Agent tools | MCP server hosted by the daemon | Common provider-neutral coordination surface |
| Interactive runtime | Herdr driver | Reuses panes, layouts, sessions, agent detection, attach, and automation |
| Source control | Installed Git CLI | Matches user Git behavior and supports worktrees without reimplementing Git |
| Search | SQLite FTS5 | Adequate deterministic retrieval without another service |
| TUI | Bubble Tea v2.0.8, Bubbles v2.1.1, Lip Gloss v2.0.6 | One Go-native event loop and renderer in the existing binary |
| Web workbench (M21) | React, TypeScript, and Vite; static output embedded in Go | Rich local planning, conversation, graphs, evidence, and agent inspection with no production Node process or Electron |
| Browser transport (M21) | Loopback HTTP JSON and SSE; WebSocket only for terminal streams | Canonical request/invalidations remain separate from optional untrusted PTY bytes |
| Logging | Structured logs with redaction | Debuggable local operation and future telemetry bridge |
| Metrics/tracing | Optional OpenTelemetry hooks | Standard observability without requiring a collector |

The SQLite driver and generated query code are checked in for reproducible offline
builds. `sqlc` is a development-time generator, not a runtime dependency. Its
version is pinned and source/output drift is checked without network access.
The current JSON Schema corpus is embedded in the binary and compiled lazily by
the pinned validator; no schema or validator is fetched at runtime.

## Why Go for the core

Crewfold is primarily a long-running local systems tool: it manages processes,
sockets, file watchers, SQLite transactions, queues, and terminal integrations. Go
fits that shape and produces an installable binary without requiring users to
manage Node or Python environments.

Rust would provide tighter control but impose more implementation complexity at
this stage. TypeScript would speed some integrations but makes a durable local
daemon and subprocess tree operationally heavier. Python is excellent for
experiments and model libraries, but is not the preferred distribution boundary.

Adapters may still use another language when a provider's supported SDK requires
it. They communicate through the versioned adapter protocol rather than entering
the core process.

## Storage model

One SQLite database initially contains:

- normalized current-state tables;
- an immutable coordination event journal;
- command idempotency records;
- durable worker queues and leases;
- full-text indexes for accepted knowledge;
- current schema-baseline metadata.

Use WAL mode, foreign keys, busy timeouts, and short transactions. Blob-sized
artifacts belong in a content-addressed local directory with metadata in SQLite.
Database backup should use SQLite's online backup mechanism, not filesystem copying
of a live file.

## Protocol

The daemon API should support request/response commands and queries plus a resumable
event stream. JSON is sufficient for the local MVP and makes debugging and adapter
development easy. Unix socket permissions provide the first transport boundary;
run-scoped capabilities provide agent authorization.

MCP is the agent-facing facade, not the internal event protocol. Human clients and
adapters can use the native local API without pretending to be MCP clients.

## Monorepo layout

```text
crewfold/
├─ cmd/crewfold/              # single CLI/daemon entry point
├─ internal/                  # core Go packages, private by default
├─ protocol/                  # Current JSON Schemas and conformance fixtures
├─ integrations/
│  ├─ herdr/                  # runtime driver
│  └─ providers/              # provider adapters and conformance fixtures
├─ web/                       # M21 embedded local workbench and reviewed product mock
└─ docs/                      # product and architecture contract
```

Keep Go code in one module until a proven reason to split it. If the web console or
TypeScript SDK begins, add a minimal `pnpm` workspace for only those packages. Do
not add a monorepo task runner before multiple build graphs exist.

## Human interfaces

### Implemented through M20

- `crewfold` CLI for commands and scripting;
- `crewfold ui` terminal dashboard;
- Herdr for live panes, layouts, and direct interaction;
- MCP for agent participation.

### M21

The owner-local browser workbench becomes the primary human experience for
onboarding, intent, planning, execution, dependency graphs, mailboxes, context,
agent inspection, decisions, evidence, and briefings. It calls canonical daemon
APIs and is embedded in the Go binary. CLI remains complete for automation,
recovery, diagnosis, and advanced administration; the TUI remains an operational
and SSH fallback; Herdr is optional interactive runtime infrastructure.

## Not in the MVP stack

- PostgreSQL;
- Redis, NATS, Kafka, or another broker;
- Kubernetes or containers as a required runtime;
- an embedding service or dedicated vector database;
- Electron;
- a hosted identity provider;
- a workflow engine;
- microservices;
- a custom terminal emulator or multiplexer;
- provider SDKs embedded directly in the domain layer.

Each can become appropriate in organization mode, but adding it now would create
operations without validating product behavior.

## Organization-mode evolution

When multiple real users and machines arrive:

| Local component | Possible evolution |
| --- | --- |
| SQLite | PostgreSQL authority plus local cache/replica |
| Unix socket | Authenticated TLS API and streaming transport |
| Local actor | User/service identity with team membership |
| Local worker | Registered execution node with capabilities |
| In-process queues | Durable distributed queue only when needed |
| File policy | Organization policy service and delegated admin |
| Local audit | Central retention, export, and compliance controls |

The local daemon remains useful as the node-side runtime and offline coordinator.
