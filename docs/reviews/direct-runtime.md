# Milestone review — Supervised direct subprocess runtime

## Identity

- Milestone: `M6 — Direct subprocess runtime`
- Review status: `passed`
- Implementation commits: `2c4043dd86bc1c22938184b3a65835b9754f7db0`,
  `951485b894273130941ae1b3a39a76a7267e2c15`
- Reviewer: Crewfold local implementation gate
- Date: `2026-08-12`

## Demonstrable outcome

- User-visible capability: execute the deterministic run/task/handoff contract
  through a real local process in the selected checkout; observe capped logs and
  omitted bytes; distinguish exit, timeout, graceful stop, forced stop, and lost
  process identity; and reconcile a child that continues across daemon restart.
- Acceptance scenario path: `test/scenarios/direct-runtime/run.sh`
- Exact command: `./scripts/check.sh`
- Expected result: formatting, vet, unit, migration, protocol, race, and all seven
  capability-named built-binary scenarios pass; direct execution prints
  `Direct subprocess acceptance: PASS`; no model, credential, remote, or network
  service is required.
- Observed result: passed on Linux/amd64 with Go 1.26.5. The public CLI/API
  completed, blocked/resumed, rejected insufficient evidence, failed before
  launch, diagnosed non-zero exit and timeout, stopped both graceful and
  uncooperative processes, bounded both output streams, rejected a caller-selected
  working directory, and completed exactly once after daemon restart.

## Test evidence

| Suite | Command | Result | Evidence |
| --- | --- | --- | --- |
| Complete local gate | `./scripts/check.sh` | passed | `gofmt`, vet, all Go tests, race detector, and seven built-binary scenarios |
| Unit | `go test ./internal/execution` | passed | Environment allowlist, output bounds, API redaction, strict fixture reports, start failure, timeout propagation, missing supervisor identity, and fake-runtime command rejection |
| Store/migration | `go test ./internal/store` | passed | Stop/lost invariants, retained capacity, schema 4→5 upgrade, and preservation of a queued active run and timeline |
| Protocol | `go test ./protocol` | passed | Unique valid schema IDs/references, result-constant agreement, and semantic validation of all direct-runtime fixtures |
| Component | `go test ./internal/daemon` | passed | Real child completion/crash/timeout/forced stop, bounded output, cleanup before provider-bind failure, and fresh-driver restart reconciliation |
| CLI | `go test ./internal/cli` | passed | Stop/log argument bounds, explicit graceful intent, and structured log/stop requests |
| Black-box acceptance | Direct subprocess scenario via check script | passed | Built binary and public CLI/API exercise success, evidence rejection, block/resume, exit, timeout, stop, output, working-directory policy, and restart |
| Race | `go test -race ./...` via check script | passed | Daemon worker, detached supervisor integration, SQLite paths, polling, shutdown, and prior concurrency remain race-clean |
| Earlier capabilities | Six earlier scenarios via check script | passed | Build, daemon, workspace, source, durable coordination, and deterministic execution remain green |
| Live conformance | N/A | passed by explicit exclusion | Paid providers, Herdr, MCP, network calls, and credentials are outside this capability |

## Failure proof

- Injected failures: provider preparation refusal; provider binding rejection after
  runtime launch; exit code 17 before completion; timeout with ignored termination;
  ignored graceful stop; excessive stdout and stderr; insufficient completion
  evidence; caller-supplied working-directory option; disallowed inherited
  environment; missing/mismatched supervisor identity; and daemon termination while
  the child remains active.
- Injection seams/barriers: fixture process controls, strict launch specification,
  runtime state files, Linux process-start identity, provider adapter `Bind`,
  post-launch binding persistence, direct stop requests, and the existing durable
  worker queue/restart boundary.
- Expected diagnosis and recovery: definite pre-launch refusal becomes
  `start_failed`; known provider-bind failure stops the runtime before capacity is
  released; unknown cleanup becomes `lost`; crash and timeout remain distinct;
  ignored termination records `stop_forced`; evidence rejection creates no
  handoff; restart reconciles the original opaque handle without duplicate launch.
- Observed diagnosis and recovery: all outcomes matched. Missing process identity
  produced `OutcomeUnknownError`; store tests converted uncertainty to `lost`,
  blocked the task, and retained assignment/checkout/concurrency. Provider binding
  rejection left the fake runtime observably stopped before the task returned to
  `assigned`. The restart case retained one run and one accepted handoff.

## Persistence and recovery

- Durable state introduced or changed: schema version 5 adds `stopping`, `stopped`,
  and `lost` run states plus stop-grace and forced-stop facts. The run-owned tables
  and live-run indexes are rebuilt to preserve uncertainty as capacity-consuming.
- Runtime state: each direct run has an owner-only directory containing an
  immutable launch specification, atomically replaced supervisor state, capped
  stdout/stderr files, and an optional stop request. SQLite stores only the opaque
  `direct:<run-id>` binding and coordination facts.
- Restart/crash points tested: the daemon stops after the first child report while
  the supervisor and child continue; a fresh daemon and fresh driver reconcile the
  stored handle, consume the remaining structured report, and complete once.
  Existing requested-intent and post-launch/pre-ack barriers also remain green.
- Reconciliation outcome: final state is accepted only from matching run/schema
  identity and, while live, matching Linux PID start identity. Missing identity or
  an unacknowledged stop cannot become success and does not release capacity.
- Migration fixture: the store test constructs representative schema-v4 run,
  queue, and timeline records, upgrades them through the checked-in
  `005_direct_runtime.sql`, and proves the active work remains claimable.
- Backup/restore impact: a coherent live backup must include SQLite through its
  online backup mechanism and the direct-runtime state directory. There is no
  backup command or down migration; rollback requires a compatible pre-upgrade
  backup.

## Security and autonomy

- New actions/capabilities: the local owner can start Crewfold's fixed fixture
  provider as a real process, read bounded logs, and request a revision-checked
  graceful stop with forced fallback.
- Allowed/denied scope: working directory comes only from the placed registered
  checkout; the CLI exposes no executable or working-directory override; inherited
  environment is limited to path, locale, temporary-directory, timezone, and run
  identity; fake runtimes reject process commands they cannot supervise.
- Secret/redaction impact: stdout/stderr API reads heuristically redact common
  secret-like assignments. The independently capped raw files are owner-only but
  can contain provider-emitted secrets and must be treated as sensitive; they are
  never promoted into shared context.
- Process safety: supervisor and child identities include Linux `/proc` start
  identity before process-group signaling. The acceptance cleanup reads only its
  temporary state and kills only PIDs whose command line matches its exact binary.
- External side effects: tests create only isolated Crewfold state, fixture Git
  repositories, and provider-free local processes. They make no network call and
  do not mutate the fixture checkout.
- Human approval boundary: every run start, resume, and stop is owner-invoked.
  There is no arbitrary project command, provider credential, automatic source
  mutation, push, merge, deployment, message, or scheduler decision.

## Compatibility

- API/schema changes: additive protocol-v1 `run.logs` and `run.stop` methods;
  bounded-log schemas; new run states and stop fields; and optional bounded process
  controls in the testing scenario contract.
- Storage changes: forward-only schema migration 4→5. Older binaries refuse the
  newer `user_version`; rollback requires a pre-upgrade backup.
- Adapter compatibility: runtime drivers now expose inspect, stop, and logs;
  provider observation receives a runtime snapshot. The runtime/provider axes
  remain independent, while incompatible launch specifications fail explicitly
  instead of creating permanently stuck runs.
- Source-layout compatibility: direct execution uses the checkout chosen by the
  existing placement contract. Adjacent standalone clones and linked worktrees
  remain equivalent; there is no `git worktree` assumption or automatic source
  mutation.
- Earlier capability scenarios rerun: all pass unchanged.

## Known limitations and deferrals

- The only direct provider is fixed, provider-free fixture code. Arbitrary project
  commands, Codex, Claude Code, generic terminals, and Herdr remain disabled.
- This is Linux-first process supervision, not an OS sandbox. It does not restrict
  filesystem access outside the working directory, network access, CPU, memory,
  subprocess count, or system calls.
- Redaction is heuristic and occurs at the API boundary. Raw owner-only capture
  files may contain sensitive output; encrypted secret handling and provider
  credential injection are deferred.
- A `lost` run deliberately retains capacity, but there is not yet an owner command
  to attest cleanup and resolve it. Manual deletion or silent reuse is not allowed.
- Completed runtime directories are not pruned and a live backup is not yet
  coordinated. Retention, garbage collection, backup, and restore arrive before
  personal beta.
- Direct blocked/resume testing consumes queued structured fixture reports. Live
  bidirectional agent reporting, scoped input, capability authentication, and
  context packets belong to M7.
- There is no attach, interactive input, usage accounting, resource budget
  enforcement, heartbeat, or general capability negotiation yet.

## Repository hygiene

- Working tree clean after implementation acceptance: yes.
- No leaked processes/sockets/temp directories: yes; exact process inspection
  found no surviving supervisor or fixture worker.
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
- Next milestone: `M7 — Run-scoped MCP and briefing`.
