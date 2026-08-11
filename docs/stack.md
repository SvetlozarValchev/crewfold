# Technology stack

## Recommendation

Build the local core as a single Go binary backed by SQLite. Expose a versioned
local API and MCP server, integrate Herdr as the primary runtime, and defer the web
console until the control plane works end to end.

M0 through M2 implement the Go binary, local Unix transport, and SQLite storage.
Later rows remain the proposed baseline until their milestone begins.

## Core

| Area | Choice | Reason |
| --- | --- | --- |
| Daemon and CLI | Go, current supported stable release | Single binary, strong process/concurrency support, low idle cost |
| Local database | SQLite in WAL mode through `github.com/ncruces/go-sqlite3` | Transactional, CGO-free, portable, and operationally trivial at personal scale |
| SQL access | Explicit SQL plus generated typed queries | Keeps schema and hot queries visible |
| Migrations | Ordered embedded SQL migrations | Reproducible upgrades and simple backups |
| Local transport | JSON messages over a Unix domain socket | Inspectable, stream-capable, user-local by default |
| Schema | JSON Schema with generated language types | Versioned contracts for CLI, adapters, SDKs, and fixtures |
| Agent tools | MCP server hosted by the daemon | Common provider-neutral coordination surface |
| Interactive runtime | Herdr driver | Reuses panes, layouts, sessions, agent detection, attach, and automation |
| Source control | Installed Git CLI | Matches user Git behavior and supports worktrees without reimplementing Git |
| Search | SQLite FTS5 | Adequate deterministic retrieval without another service |
| TUI | Go terminal UI library, likely Bubble Tea | Same deployment unit; good status and inbox experience |
| Logging | Structured logs with redaction | Debuggable local operation and future telemetry bridge |
| Metrics/tracing | Optional OpenTelemetry hooks | Standard observability without requiring a collector |

The SQLite driver is vendored for reproducible offline builds. Other specific Go
libraries remain open until their implementation spike. The architecture depends
on interfaces and behavior, not library branding.

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
- schema migration state.

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
├─ protocol/                  # JSON Schemas and compatibility fixtures
├─ integrations/
│  ├─ herdr/                  # runtime driver
│  └─ providers/              # provider adapters and conformance fixtures
├─ web/                       # deferred browser console
└─ docs/                      # product and architecture contract
```

Keep Go code in one module until a proven reason to split it. If the web console or
TypeScript SDK begins, add a minimal `pnpm` workspace for only those packages. Do
not add a monorepo task runner before multiple build graphs exist.

## Human interfaces

### First

- `crewfold` CLI for commands and scripting;
- `crewfold watch` or `crewfold ui` terminal dashboard;
- Herdr for live panes, layouts, and direct interaction;
- MCP for agent participation.

### Later

A local browser console can visualize dependency graphs, mailboxes, context
revisions, and large fleets. It should call the same API and remain optional.

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
