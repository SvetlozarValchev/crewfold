# Milestone review — Deterministic run execution

## Identity

- Milestone: `M5 — Deterministic fake-agent vertical slice`
- Review status: `passed`
- Implementation commit: `ba57abefc94743ea9dbf0a4c1e1bb6addffaf242`
- Reviewer: Crewfold local implementation gate
- Date: `2026-08-12`

## Demonstrable outcome

- User-visible capability: consume a task's leased assignment, choose and explain
  a writable checkout placement, asynchronously execute a bounded fake scenario,
  persist normalized progress/blockage/completion, evaluate evidence, create an
  accepted handoff, and resume durable work after daemon restart.
- Acceptance scenario path: `test/scenarios/deterministic-execution/run.sh`
- Exact command: `./scripts/check.sh`
- Expected result: formatting, vet, unit, migration, protocol, race, and every
  capability-named black-box scenario pass; deterministic execution prints
  `Deterministic execution acceptance: PASS`; no external process, model,
  credential, network service, remote, or source mutation is involved.
- Observed result: passed on Linux/amd64 with Go 1.26.5. The public CLI/API placed
  a run on an explicit adjacent standalone `world-engine-2` clone, completed with
  evidence and a handoff, restored and resumed a blocked run, preserved an active
  checkpoint across restart, diagnosed runtime start failure, and rejected a
  completion missing required evidence.

## Test evidence

| Suite | Command | Result | Evidence |
| --- | --- | --- | --- |
| Complete local gate | `./scripts/check.sh` | passed | Formatting, vet, all Go tests, race detector, and six built-binary scenarios |
| Unit/store | `go test ./internal/execution ./internal/store` | passed | Strict scenario parsing, idempotent runtime launch, acceptance, placement, capacity, atomic intent, state transitions, queue, timeline, and handoff |
| Store/migration | `go test ./internal/store` | passed | Schema version 4 plus representative coordination-record upgrade preserving task, dependency, and active assignment data |
| Protocol | `go test ./protocol` | passed | Unique valid schema IDs/references, constant agreement, provider-neutral methods, and semantic validation of all five checked-in scenarios |
| Component | `go test ./internal/daemon` | passed | Real Unix socket and SQLite worker; success, block/resume, review, start failure, requested-intent restart, and post-launch/pre-ack restart |
| Black-box acceptance | Deterministic execution scenario via check script | passed | Real binary exercises public CLI/API only, including two daemon restarts and adjacent-clone placement |
| Race | `go test -race ./...` via check script | passed | Worker, fake runtime launch map, server shutdown, API polling, and existing concurrent writers are race-clean |
| Clean module cache | `GOMODCACHE=<empty> GOPROXY=off go test -count=1 ./...` | passed | Vendored offline build/test needs no downloaded module |
| CGO independence | `CGO_ENABLED=0 GOPROXY=off go test -count=1 ./...` | passed | Run storage and adapters retain the portable SQLite boundary |
| Repetition | `go test -count=5 ./internal/store ./internal/daemon` | passed | Transaction, queue, worker, restart, and socket paths pass repeatedly |
| Scenario repetition | Three deterministic-execution scenario runs | passed | No lifecycle, polling, restart, placement, or cleanup flake |
| Earlier capabilities | Five earlier scenarios via check script | passed | Build, daemon, workspace, source, and durable coordination behavior remain green |
| Live conformance | N/A | passed by explicit exclusion | Paid/installed providers and real runtimes are outside this capability |

## Failure proof

- Injected failures: malformed/unknown/trailing scenario content; unavailable
  adapter registry entry; read-only placement; agent concurrency saturation;
  duplicate live run; runtime start refusal; missing completion evidence; accepted
  completion without a handoff; and transaction interruption after run projection
  or event append.
- Injection seams/barriers: strict fake-scenario loader, store invariants,
  deterministic fake adapter, `MutationAfterProjection`, `MutationAfterEvent`,
  `after_run_starting`, and `after_runtime_launch` worker hooks.
- Expected diagnosis and recovery: stable `invalid_run`, `run_conflict`,
  `placement_unavailable`, `adapter_unavailable`, `start_failed`, and
  `changes_requested` outcomes; interrupted intent leaves no run, queue item,
  timeline, event, or idempotency record; post-launch restart replays the stable
  run operation ID and returns the existing runtime binding.
- Observed diagnosis and recovery: all outcomes matched. The same injected fake
  runtime recorded exactly one launch across daemon death after effect and before
  acknowledgement. Start failure retained the task assignment while releasing
  run capacity. Rejected evidence retained assignment and created no accepted
  handoff.

## Persistence and recovery

- Durable state introduced or changed: schema version 4 expands task outcomes and
  adds `runs`, `run_jobs`, `run_timeline`, and `run_handoffs`, plus live-run,
  placement/capacity, queue, and timeline indexes.
- Restart/crash points tested: requested intent before any worker effect;
  starting state after runtime launch but before `run.started`; blocked state; an
  explicitly paused active checkpoint; and terminal completed, review, and
  start-failed state queried after later restarts.
- Reconciliation outcome: one stable run ID drives idempotent launch; requested
  and starting jobs are reclaimed; blocked/active runs preserve their observation
  cursor; accepted handoffs remain singular; terminal jobs do not rerun.
- Migration fixture: `internal/store/testdata/coordination-upgrade.sql` composes
  representative schema-v3 coordination records without naming a file after the
  milestone. Migration preserves IDs, revisions, dependency edge, and active
  assignment while adding empty run tables.
- Backup/restore impact: online backup still must include the live WAL. There is no
  backup command or down migration; rollback requires a compatible pre-upgrade
  backup.

## Security and autonomy

- New actions/capabilities: local owner can start, query, watch, list, and resume
  fake runs and inspect task timelines.
- Allowed/denied scope: a run requires a current task assignment; enabled matching
  agent; registered runtime/provider adapters; agent concurrency; same-project,
  available, writable checkout; expected revision; and one live run per task.
- Secret/redaction impact: scenarios, normalized messages, evidence labels,
  placement reasons, opaque handles, and handoffs become local durable data. Files
  are bounded and strictly decoded. Raw transcripts, environment variables,
  credentials, and source contents are not captured.
- External side effects: only Crewfold's owner-only socket/database and read-only
  Git inspection used during project/checkout registration. The fake runtime does
  not create a process and the acceptance scenario does not mutate checkouts.
- Human approval boundary: the local owner creates and assigns tasks and starts or
  resumes every run. There is no autonomous scheduler selecting tasks, manager,
  supervisor, messaging, merge, push, CI, or external notification.

## Compatibility

- API/schema changes: additive protocol-v1 methods `run.start`, `run.show`,
  `run.list`, `run.resume`, and `task.timeline`; domain/local/fake-scenario JSON
  Schemas publish run, placement, timeline, and handoff records.
- Storage changes: forward-only schema migration 3→4. Older binaries correctly
  refuse the newer `user_version`; rollback requires a pre-upgrade backup.
- Adapter compatibility: runtime and provider are independent interfaces and
  registries. Core placement/state code does not branch on Codex, Claude, Herdr,
  or worktree names.
- Source-layout compatibility: acceptance explicitly places on the adjacent
  standalone `world-engine-2` clone. Linked worktrees remain eligible through the
  same checkout contract; no run code invokes `git worktree` or derives ownership
  from `.git` layout.
- Earlier capability scenarios rerun: all pass unchanged.

## Known limitations and deferrals

- Only deterministic fake adapters exist. No child process, Herdr pane, Codex,
  Claude Code, MCP endpoint, provider prompt, transcript, usage, or native session
  is created.
- One daemon worker polls one SQLite queue. There is no multi-worker claim
  throughput, heartbeat, live lease renewal, backoff/dead-letter policy, or load
  result yet.
- The fake scenario is a testing contract, not a general workflow language.
  `review`/`changes_requested` is inspectable and retains assignment, but an
  evidence-correction/retry command is deferred.
- There is no run attach, interrupt, stop, timeout, graceful termination, output
  capture, usage accounting, budget enforcement, or runtime health probe.
- Placement enforces checkout write mode against live runs, but declared path
  claims, observed diffs, overlap detection, and automatic source isolation are
  later capabilities.
- `task.timeline` currently combines run/timeline facts; a unified task-event view
  and resumable event subscription remain deferred.

## Repository hygiene

- Working tree clean after implementation acceptance: yes.
- No leaked processes/sockets/temp directories: yes.
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
- Next milestone: `M6 — Direct subprocess runtime`.
