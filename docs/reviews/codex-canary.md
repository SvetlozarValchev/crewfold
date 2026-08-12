# Milestone review — Codex provider canary

## Identity

- Milestone: `M10 — Codex canary`
- Review status: `pending`
- Commit: `d8f6aac5060eb31e380c95c9d01c8aa2dddadd49`
- Reviewer: `automated offline acceptance; owner consent required for live provider acceptance`
- Date: `2026-08-12`

## Demonstrable outcome

- User-visible capability: probe an installed Codex CLI/auth session, launch the
  Codex provider on direct or Herdr runtime with a required run-scoped MCP
  binding, and accept its structured completion handoff without granting terminal
  output completion authority.
- Acceptance scenario path: `test/scenarios/codex-provider/run.sh`
- Exact command: `./scripts/check.sh`
- Expected result: all earlier suites pass and the final offline scenario reports
  `Codex provider acceptance: PASS` without credentials, network access, or model
  usage.
- Captured structured result/artifact: provider doctor schema
  `urn:crewfold:schema:provider:codex-probe:v1`, `codex-provider:v1:<run-id>`
  binding, `run.tool_called`, `run.report_received`, accepted evidence, handoff,
  and terminal `completed` state.

## Test evidence

| Suite | Command | Result | Artifact/log |
| --- | --- | --- | --- |
| Unit | `./scripts/go.sh test ./internal/execution ./internal/cli ./internal/daemon ./protocol` | passed | probe, manifest, bridge, boundary normalization, CLI, daemon, schema tests |
| Store/migration | `./scripts/check.sh` | passed | existing store/migration suite; no migration added |
| Protocol | `./scripts/go.sh test ./protocol` | passed | provider-doctor JSON Schema plus all published schemas |
| Component | `./scripts/go.sh test ./internal/execution ./internal/daemon` | passed | real Unix STDIO bridge and recorded Codex command boundary |
| Black-box acceptance | `test/scenarios/codex-provider/run.sh` | passed | recorded Codex probe → launch → briefing → completion → handoff |
| Full offline/race gate | `./scripts/check.sh` | passed | vet, all tests, race tests, and eleven black-box scenarios |
| Installed probe | built binary `doctor --provider codex --output json` | passed | local `codex-cli 0.0.0`, required flags present, `Logged in using ChatGPT` |
| Live conformance | `CREWFOLD_LIVE_CODEX=1 CREWFOLD_ALLOW_MODEL_CALLS=1 ./test/live/codex/run.sh` | pending explicit owner opt-in | not run; command can consume provider/network usage |

## Failure proof

- Injected failure: recorded `codex login status` exits non-zero; empty MCP access
  is returned before launch; recorded terminal output contains required-MCP startup
  failure or an approval boundary.
- Injection seam/barrier: command runner, run-capability preparer, and normalized
  provider observation.
- Expected diagnosis and recovery: `doctor` names authentication and tells the
  operator to log in; preparation names the missing socket/capability file; MCP
  startup becomes `provider_observation_failed`; explicit approval/permission
  output becomes blocked and never completion.
- Observed diagnosis and recovery: component and black-box assertions passed. No
  generic timeout was used for the injected auth/MCP cases.
- Operation/event IDs: temporary deterministic run IDs and durable
  `run.tool_called`/`run.report_received` events created by the acceptance script.

## Persistence and recovery

- Durable state introduced or changed: no schema change; existing run provider
  handle, report, tool-call audit, handoff, and event records are reused.
- Restart/crash points tested: all earlier daemon/runtime restart suites rerun;
  no new provider-private durable saga is introduced.
- Reconciliation outcome: runtime drivers continue to own process reconciliation;
  a Codex completion report is accepted only after the provider process settles.
- Migration fixture added: N/A.
- Backup/restore impact: N/A; only existing rows are written.

## Security and autonomy

- New actions/capabilities: execute an allowlisted Codex CLI and a hidden Crewfold
  STDIO bridge in the assigned checkout.
- Allowed, denied, wrong-scope, and approval-required tests: existing MCP scope
  tests rerun; bridge injection and approval normalization have dedicated tests;
  missing auth and missing MCP access are rejected.
- Secret/redaction impact: only `CODEX_HOME`, socket path, and capability-file path
  cross the runtime environment. The token is read by the bridge, injected only on
  the owner-only socket, and asserted absent from launch arguments and logs.
- External side effects: default tests use a recorded CLI and no network/model.
  The live script owns a disposable Git repository and dedicated Herdr session,
  permits one file change, configures no remote, and checks that no commit exists.
- Human approval boundary: two explicit environment flags are required before the
  live script may call a model. Continuing normal development is not consent.

## Compatibility

- API/schema changes: additive provider doctor response schema and additive
  `doctor --provider codex` CLI form.
- Adapter/runtime compatibility changes: the Codex adapter emits the existing
  `CommandSpec` and works with both direct and Herdr runtimes; core scheduling has
  no provider-name branch. Terminal Herdr snapshots now include bounded final pane
  output for provider-boundary diagnosis.
- Earlier milestone scenarios rerun: all M0–M9 scenarios passed under
  `scripts/check.sh`.
- Upgrade/rollback impact: rollback removes the registered Codex adapter/doctor;
  SQLite needs no downgrade.

## Known limitations and deferrals

- Known limitation: the implemented provider is headless, one-shot, and
  ephemeral. Herdr attach observes its terminal, but runtime prompt delivery is
  not active-turn Codex steering.
- Explicitly deferred behavior: native thread persistence/resume, app-server
  ownership, turn steering, provider-aware mailbox wake, usage accounting, and
  Claude conformance.
- Follow-up milestone or issue: close this review by running the live canary with
  owner consent; only then begin `M11 — Claude canary`.

## Repository hygiene

- Working tree clean after acceptance scenario: yes at implementation commit.
- No leaked processes/sockets/temp directories: offline acceptance cleanup passed.
- No paid/network call in default tests: yes.
- Documentation matches behavior: yes; offline completion and live pending state
  are stated separately.

## Decision

- Exit gate satisfied: `no` — the required real Codex run has not been authorized
  or executed.
- Waivers and accepting authority: none.
- Next milestone entry criteria met: `no`.
- Notes: implementation is ready and all non-provider-variable evidence is green.
  A later live pass should update this record to `passed`, capture its command and
  result, and mark M10 complete without changing the adapter merely to satisfy the
  audit.
