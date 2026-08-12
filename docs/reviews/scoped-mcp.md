# Milestone review — Run-scoped MCP and briefing

## Identity

- Milestone: `M7 — Run-scoped MCP and briefing`
- Review status: `passed`
- Implementation commit: `99e9791d39e2c0b3e36333f366a4fd84bcbaf6ef`
- Reviewer: Crewfold local implementation gate
- Date: `2026-08-12`

## Demonstrable outcome

- User-visible capability: build and explain an immutable task briefing, bind it
  to exactly one run, launch a direct fixture provider with a run-scoped
  capability, and let that provider read its briefing and report progress,
  artifacts, blockage, status, or proposed completion through MCP.
- Acceptance scenario path: `test/scenarios/scoped-mcp/run.sh`
- Exact command: `./scripts/check.sh`
- Expected result: formatting, vet, unit, migration, protocol, race, and all eight
  capability-named built-binary scenarios pass; scoped execution prints
  `Scoped MCP capability acceptance: PASS`; no model, credential, remote, or
  network service is required.
- Observed result: passed on Linux/amd64 with Go 1.26.5. The public CLI/API built,
  explained, bound, restored, and inspected one immutable context packet. The
  direct fixture used only MCP for briefing, cross-run denial, artifact,
  idempotent progress, and completion reporting. The durable worker applied the
  accepted reports in order and completed the run exactly once.

## Test evidence

| Suite | Command | Result | Evidence |
| --- | --- | --- | --- |
| Complete local gate | `./scripts/check.sh` | passed | `gofmt`, vet, all Go tests, race detector, and eight built-binary scenarios |
| Store/migration | `go test ./internal/store` | passed | Stable packet selection/hash, scoped binding, report/artifact idempotency, ordered application, expiration/inactivity, schema 4→5→6 migration, and no invented authority for old runs |
| Protocol | `go test ./protocol` | passed | Unique valid schema IDs/references and semantic validation for context and MCP mutation contracts |
| Component | `go test ./internal/daemon` | passed | Private deterministic capability material, tamper rejection, restart derivation, scoped MCP success, cross-run denial, stopped-run denial, and audit facts |
| CLI | `go test ./internal/cli` | passed | Context build/show/explain and explicit context binding use structured public requests |
| Black-box acceptance | Scoped MCP scenario via check script | passed | Built binary and public CLI/API exercise briefing, direct MCP reports, cross-run denial, report deduplication, log non-disclosure, and restart restoration |
| Race | `go test -race ./...` via check script | passed | MCP connections, daemon workers, SQLite report queues, direct supervision, and earlier concurrency remain race-clean |
| Earlier capabilities | Seven earlier scenarios via check script | passed | Build, daemon, workspace, source, coordination, deterministic execution, and direct runtime remain green |
| Live conformance | N/A | passed by explicit exclusion | Paid providers, Herdr, network calls, credentials, messaging, and retrieval are outside this capability |

## Failure proof

- Injected failures: a valid run capability requests another run's resource; a
  stopped run reuses an established MCP connection; a capability expires; token
  material is tampered with; one idempotency key is reused with changed report
  content; and a packet is offered to a different task.
- Injection seams/barriers: MCP resource URIs, run lifecycle changes, a controlled
  store clock, HMAC validation, command hashes, binding uniqueness, revision
  checks, and size/count limits at the protocol and store boundaries.
- Expected diagnosis and recovery: wrong-scope requests produce `out_of_scope`;
  expired or terminal authority is denied; tampering never authenticates; exact
  report/artifact replays return the original durable record while changed
  content conflicts; mismatched packets prevent launch; and every scoped resource
  read or tool call is auditable without persisting the capability token.
- Observed diagnosis and recovery: all outcomes matched. The black-box fixture's
  wrong-run probe produced `run.tool_denied`; its duplicated progress mutation
  produced one durable report event and one report identity. Component tests
  observed both cross-run and stopped-run denial events, and store tests proved
  expiry, scoped binding, replay, and inactive-authority behavior.

## Persistence and recovery

- Durable state introduced or changed: schema version 6 adds immutable
  `context_packets`, unique `run_context_bindings`, expiring `run_capabilities`,
  ordered `run_reports`, bounded `run_artifacts`, and `run_tool_calls`.
- Atomic boundaries: run creation either validates and binds an explicit packet or
  builds and binds one in the same transaction. Applying a queued MCP report and
  its run/task state transition is one transaction. Exact retries return their
  original records rather than appending duplicate facts.
- Restart/crash points tested: a packet is built and explained, the MCP run
  completes, the daemon stops, and a fresh daemon returns byte-equivalent
  explanation data and the same run binding. Reconstructing the capability
  manager from the same private node key derives the same token without storing
  it in SQLite.
- Reconciliation outcome: completed runs restore with the same context packet ID;
  capabilities authorize only live `starting`, `active`, or `blocked` runs and
  therefore do not regain authority after terminal restoration.
- Migration fixture: the store test upgrades representative schema-v4 state
  through schema 5 and 6, preserves its prior run/timeline facts, and verifies the
  scoped tables remain empty. Crewfold does not invent context or capability
  authority for a run created by an older schema.
- Backup/restore impact: the SQLite database and owner-only node key are both
  required to preserve live scoped authority. Runtime capability files can be
  deterministically recreated from that key. There is no backup command or down
  migration; rollback requires a compatible pre-upgrade backup.

## Security and autonomy

- New actions/capabilities: one live run can read its own briefing/status and
  submit only bounded, typed progress, blockage, artifact, and completion
  proposals. The existing durable worker remains the authority that validates and
  applies state transitions.
- Authentication: a per-node 256-bit key and per-run HMAC capability are stored in
  owner-only regular files. The child receives a capability file path and socket
  path; neither the token nor node key enters the launch specification or SQLite.
  Symlinked or loosely permissioned key/token files are refused.
- Scope: the authenticated run ID is derived from the capability, never accepted
  from tool arguments. MCP exposes only that run's briefing/context resources and
  fixed tools. A capability cannot select another workspace, task, checkout,
  agent, run, command, or filesystem path.
- Audit: every authorized or denied scoped resource read and tool call creates a
  bounded tool audit fact. Reports and artifacts create separate domain facts,
  retaining idempotency and provenance without transcript ingestion.
- Same-UID limitation: Unix socket permissions and file modes protect Crewfold
  from other OS users, but this milestone is not process containment. A malicious
  child running as the Crewfold owner could inspect same-user processes/files,
  discover the owner API socket, or print its own token. Therefore only the fixed,
  trusted fixture MCP provider is enabled; arbitrary providers require a stronger
  containment or authenticated owner-API boundary.
- Human approval boundary: the owner starts and stops runs. MCP completion remains
  a proposal; there is no arbitrary source mutation, push, merge, deployment,
  message, scheduler decision, or knowledge promotion.

## Compatibility

- API/schema changes: additive local API `context.build`, `context.show`, and
  `context.explain`; optional run context selection; context packet/explanation
  schemas; MCP protocol `2025-06-18`; and bounded schemas for report, artifact,
  blockage, and completion inputs.
- Transport compatibility: owner JSON-RPC and MCP JSON-RPC share the existing Unix
  socket and are discriminated by their protocol envelopes. Existing owner
  clients and all earlier acceptance scenarios pass unchanged.
- Storage changes: forward-only schema migration 5→6. Older binaries refuse the
  newer `user_version`; rollback requires a pre-upgrade backup.
- Adapter compatibility: the direct runtime/provider separation remains intact.
  The new `fixture-mcp` provider consumes scoped environment paths; the legacy
  deterministic fixture provider remains available for earlier contracts.
- Source-layout compatibility: packet checkout identity comes from the registered
  placement contract. Adjacent standalone clones and linked worktrees remain
  equivalent; no `git worktree` topology is assumed.

## Known limitations and deferrals

- Only the trusted `fixture-mcp` client uses the surface. Codex, Claude Code,
  arbitrary commands, interactive input, and Herdr remain disabled.
- MCP currently shares the owner API socket. Protocol-level least authority is
  implemented, but hostile same-UID process isolation is not.
- Context packets contain the base role/task/checkout/dependency/policy/reporting
  contract only. Messages, claims, canonical knowledge, retrieval/RAG, context
  deltas, transcript references, and curator decisions are explicitly excluded.
- Reports are queued and applied durably, but agents cannot yet communicate with
  each other. Durable mail, offline delivery, wake-up, read, and acknowledgement
  belong to M8.
- Artifact bodies are bounded SQLite records for proof of the tool contract.
  Content-addressed large artifact storage, retention, redaction workflows, and
  garbage collection remain deferred.
- There is no general capability negotiation, rotation/revocation command,
  resource budgeting, usage accounting, or remote/multi-user authorization.

## Repository hygiene

- Working tree clean after implementation acceptance: yes.
- No leaked processes/sockets/temp directories: yes; the acceptance cleanup
  targets only its isolated runtime and validates exact binary ownership.
- No paid/network call in default tests: yes.
- Documentation and schemas match behavior: yes.
- No milestone codes in executable artifact paths, test identifiers, fixture
  values, environment variables, or temporary names: yes. Milestone codes remain
  only as planning/history prose.
- No upstream Git remote created: yes.

## Decision

- Exit gate satisfied: `yes`.
- Waivers: none.
- Next milestone entry criteria met: `yes`.
- Next milestone: `M8 — Durable two-agent messaging`.
