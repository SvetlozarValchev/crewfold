# Milestone review — Durable agents and tasks

## Identity

- Milestone: `M4 — Durable agents and tasks`
- Review status: `passed`
- Implementation commits: `7973bded9f99e965bc01a662b6b4d532e679d2c3`,
  `dbce60007de652d09862a8f673886702ba9860bc`,
  `c821ab12cc649d7807a504ee615e61796591178e`
- Reviewer: Crewfold local implementation gate
- Date: `2026-08-12`

## Demonstrable outcome

- User-visible capability: define provider-neutral agents, project objectives,
  budgets, dependency-aware tasks, optimistic revisions, and leased primary
  assignments; inspect readiness and workspace coordination status; move a task
  through assigned, active, blocked, unblocked, and cancelled states without
  launching a provider process.
- Acceptance scenario path: `test/scenarios/durable-coordination/run.sh`
- Exact command: `./scripts/check.sh`
- Expected result: formatting, vet, unit, schema, protocol, race, and every
  capability-named black-box scenario pass; durable coordination prints
  `Durable coordination acceptance: PASS`; no runtime/provider process starts.
- Observed result: passed on Linux/amd64 with Go 1.26.5. The public CLI created an
  agent, objective, budgets, tasks, a dependency, and an assignment; rejected a
  dependency cycle, not-ready assignment, double assignment, and stale revision;
  exercised the state machine; and restored byte-identical list/status/event JSON
  after daemon restart.

## Test evidence

| Suite | Command | Result | Evidence |
| --- | --- | --- | --- |
| Formatting/static | `./scripts/check.sh` | passed | `gofmt` clean and `go vet ./...` passed |
| Unit/store | `go test ./...` via check script | passed | Definitions, idempotency, budgets, state validation, readiness, cycles, assignment history, leases, and optimistic revisions |
| Race | `go test -race ./...` via check script | passed | Concurrent API writers, controlled-clock reconciliation, daemon restart, and prior packages are race-clean |
| Store/schema | `go test ./internal/store` | passed | Current-baseline coordination integrity with workspace/project/repository/checkout/event/idempotency data |
| Protocol | `go test ./protocol` | passed | Unique valid JSON Schema IDs/references, result constant agreement, and provider-neutral method names |
| Component | `go test ./internal/daemon` | passed | Real Unix socket/SQLite/Git setup, all coordination operations, concurrent writers, controlled lease expiry, and restart persistence |
| Black-box acceptance | Durable coordination scenario via check script | passed | Public binary exercises happy/failure paths and restart without test-only API access |
| Earlier capabilities | Four earlier scenarios via check script | passed | Build, daemon, persistent workspace, and projects/checkouts remain green under capability-based paths |
| Clean module cache | `GOMODCACHE=<empty> GOPROXY=off go test -count=1 ./...` | passed | Vendored offline build/test needs no downloaded module |
| CGO independence | `CGO_ENABLED=0 GOPROXY=off go test -count=1 ./...` | passed | Coordination retains the portable SQLite boundary |
| Repetition | `go test -count=10 ./internal/store ./internal/daemon` | passed | Lease, revision, API, persistence, and restart paths pass repeatedly |
| Scenario repetition | Five durable-coordination scenario runs | passed | No contention, lifecycle, persistence, or cleanup flake |
| Live conformance | N/A | passed by explicit exclusion | No model, provider, runtime, credential, remote, or paid call belongs to this capability |

## Failure proof

- Injected failures: a directed dependency cycle; an assignment before dependency
  completion; two primary assignments for one task; illegal task transitions; a
  lease moved past expiry with a controlled clock; and two concurrent updates at
  the same expected revision.
- Injection seam/barrier: real local API/CLI error paths, store clock injection,
  and simultaneous API/store writers serialized through an immediate SQLite
  transaction.
- Expected diagnosis and recovery: stable `dependency_cycle`, `task_not_ready`,
  `assignment_conflict`, `invalid_transition`, and `revision_conflict` errors;
  exactly one concurrent writer succeeds; expiry advances task/assignment
  revisions, appends an event, and preserves assignment history.
- Observed diagnosis and recovery: every stable code was observed. One writer
  succeeded and one conflicted in both store and real-daemon coverage. Assigned or
  active expired work returned to ready; blocked expired work remained blocked and
  became ready only after explicit unblock. The expired/released assignment rows
  remained queryable in component tests.
- Operation/event IDs: every successful mutation returns the durable entity/detail
  plus event sequence. API request IDs remain event correlation IDs.

## Persistence and recovery

- Durable state exercised: `agents`,
  `objectives`, `tasks`, `task_dependencies`, and `task_assignments` plus indexes
  for unique active assignment, project/status ordering, dependency traversal, and
  agent assignment queries.
- Restart/crash points tested: graceful stop/restart after definitions,
  dependencies, transitions, assignment release/expiry, and concurrent revision
  conflict. Existing projection/event atomicity barriers continue to pass.
- Reconciliation outcome: definitions, budgets, graph edges, states, revisions,
  assignment history, and events survive restart. Expired leases reconcile once;
  a second pass emits no duplicate event.
- Current-baseline tests construct representative source/workspace/event/
  idempotency and coordination rows and prove restart without loss or fabricated
  authority. A coherent live backup must include the WAL.

## Security and autonomy

- New actions/capabilities: mutate Crewfold-local coordination records only.
  `task start` is a state transition, not a process launch.
- Allowed/denied scope: definitions are scoped to a workspace; objectives/tasks to
  a workspace project; dependency edges must stay in one project; objective/task
  cross-scope references are rejected; disabled agents and non-ready tasks cannot
  receive assignments; one task cannot hold two active primary assignments.
- Secret/redaction impact: roles, provider/runtime labels, task descriptions,
  block reasons, and budgets become local durable data. No credentials, prompts,
  transcripts, environment values, or model output are read or stored.
- External side effects: none beyond Crewfold's database/socket and the already
  established read-only Git fixture inspection used to register a project.
- Human approval boundary: the local owner invokes every mutation. There is no
  scheduler, manager, supervisor, background launcher, provider adapter, source
  mutation, or external message.

## Compatibility

- API/schema changes: additive protocol-v1 methods for agent/objective/task CRUD,
  dependency/assignment/state mutations, and coordination status. Twenty-three
  local method schemas and nine domain schemas publish the new wire records.
- Storage evidence: fresh-baseline creation and canonical integrity cover the
  coordination tables and indexes.
- Adapter/runtime compatibility changes: none. Provider and runtime are opaque
  configuration strings; no adapter interface or process exists yet.
- Earlier capability scenarios rerun: all pass after scenario, protocol-test,
  review, fixture, helper, and temporary-path artifacts were renamed by capability
  rather than milestone code.
- Source-layout compatibility: no task/agent logic assumes `git worktree`.
  Adjacent standalone directories and linked worktrees remain equal registered
  checkout forms, and tasks are not prematurely coupled to either.

## Known limitations and deferrals

- There is no task completion transition yet, so dependencies can be modeled and
  explained but advance through completion only when the deterministic run loop
  introduces evidence-backed completion.
- Lease expiry is reconciled by task/status access rather than a background timer.
- `max_concurrency` and budgets are durable configuration, not yet scheduler
  enforcement. There is no assignment renewal/release command beyond cancellation
  and expiry.
- Tasks are not yet placed onto a checkout, and write modes are not enforced;
  claims and scheduling own those semantics.
- Agent instructions, teams, capabilities, messages, meetings, knowledge,
  context packets, MCP, Herdr, real providers, and organization mode remain
  deferred.
- Assignment history is durable and visible through events/component queries, but
  a dedicated public assignment-history query is not implemented.

## Repository hygiene

- Working tree clean after implementation acceptance: yes.
- No leaked processes/sockets/temp directories: yes.
- No paid/network call in default tests: yes.
- Documentation matches behavior: yes.
- No milestone codes in executable artifact paths, test identifiers, fixture
  values, environment variables, or temporary names: yes. Milestone codes remain
  only as planning/history labels in roadmap and review prose.
- No upstream Git remote created: yes.

## Decision

- Exit gate satisfied: `yes`.
- Waivers: none.
- Next milestone entry criteria met: `yes`.
- Next milestone: `M5 — Deterministic fake-agent vertical slice`.
