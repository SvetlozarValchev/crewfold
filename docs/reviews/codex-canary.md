# Milestone review — Codex provider canary

## Identity

- Milestone: `M10 — Codex canary`
- Review status: `passed`
- Implementation commits: `d8f6aac5060eb31e380c95c9d01c8aa2dddadd49`,
  `676c0bc15a31ef9b2b8233961d2b6eed696bd1c1`
- Reviewer: `automated offline acceptance plus owner-authorized live conformance`
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
| Store/schema | `./scripts/check.sh` | passed | current storage-baseline suite; no Codex-specific storage added |
| Protocol | `./scripts/go.sh test ./protocol` | passed | provider-doctor JSON Schema plus all published schemas |
| Component | `./scripts/go.sh test ./internal/execution ./internal/daemon` | passed | real Unix STDIO bridge and recorded Codex command boundary |
| Black-box acceptance | `test/scenarios/codex-provider/run.sh` | passed | recorded Codex probe → launch → briefing → completion → handoff |
| Full offline/race gate | `./scripts/check.sh` | passed | vet, all tests, race tests, and eleven black-box scenarios |
| Installed probe | built binary `doctor --provider codex --output json` | passed | local `codex-cli 0.0.0`, required flags present, `Logged in using ChatGPT` |
| Live conformance | containerized command documented in `docs/testing.md` with both model/network and external-sandbox acknowledgements | passed | `Installed Codex canary: PASS`; exact one-file diff, local check, `git diff --check`, MCP evidence, no commit, no token in logs |

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
  generic timeout was used for the injected auth/MCP cases. The first native live
  attempt also identified the host bubblewrap/AppArmor namespace boundary, and an
  early container attempt precisely identified the omitted
  `codex-code-mode-host`; both failed before being mistaken for task completion.
- Operation/event IDs: temporary deterministic run IDs and durable
  `run.tool_called`/`run.report_received` events created by the acceptance script.

## Persistence and recovery

- Durable state introduced or changed: no schema change; existing run provider
  handle, report, tool-call audit, handoff, and event records are reused.
- Restart/crash points tested: all earlier daemon/runtime restart suites rerun;
  no new provider-private durable saga is introduced.
- Reconciliation outcome: runtime drivers continue to own process reconciliation;
  a Codex completion report is accepted only after the provider process settles.
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
  The authorized live run downloaded checksum-verified Herdr 0.8.0 into temporary
  state, used the existing Codex login, and built a local Docker canary image from
  the digest-pinned Ubuntu base. Herdr, copied authentication, repository, socket,
  and run state were removed; the explicitly built local container image remains.
- External containment: this host cannot start Codex's nested bubblewrap network
  namespace. The passing route runs Codex as the current non-root UID in a
  read-only container with all Linux capabilities dropped, no setuid/setgid image
  files, a temporary copied auth/state directory, and only the disposable canary
  root mounted read-write. The matching Codex code-mode host is packaged with the
  CLI. `danger-full-access` is refused unless both the live script and daemon are
  explicitly told that this independent boundary exists.
- Human approval boundary: two explicit environment flags are required before the
  live script may call a model; external-sandbox mode requires a third assertion.
  The owner explicitly authorized the run on 2026-08-12. Continuing normal
  development is not consent.

## Compatibility

- API/schema changes: additive provider doctor response schema and additive
  `doctor --provider codex` CLI form.
- Adapter/runtime compatibility changes: the Codex adapter emits the existing
  `CommandSpec` and works with both direct and Herdr runtimes; core scheduling has
  no provider-name branch. Terminal Herdr snapshots now include bounded final pane
  output for provider-boundary diagnosis.
- Earlier milestone scenarios rerun: all M0–M9 scenarios passed under
  `scripts/check.sh`.
- Removal impact: removing the registered Codex adapter removes its doctor and
  launch path without changing SQLite.

## Known limitations and deferrals

- Known limitation: the implemented provider is headless, one-shot, and
  ephemeral. Herdr attach observes its terminal, but runtime prompt delivery is
  not active-turn Codex steering.
- Explicitly deferred behavior: native thread persistence/resume, app-server
  ownership, turn steering, provider-aware mailbox wake, usage accounting, and
  Claude conformance.
- Follow-up milestone or issue: `M11 — Claude canary` proves that the same domain
  and MCP contract survives a provider switch.

## Repository hygiene

- Working tree clean after acceptance scenario: yes at implementation commit and
  after the live verification commit.
- No leaked processes/sockets/temp directories: offline and live cleanup checks
  passed; no canary container remains running.
- No paid/network call in default tests: yes.
- Documentation matches behavior: yes; the always-offline gate and explicitly
  authorized live conformance path are stated separately.

## Decision

- Exit gate satisfied: `yes` — the owner-authorized real Codex run completed the
  same scoped MCP contract as the fixture agent.
- Waivers and accepting authority: none.
- Next milestone entry criteria met: `yes`; M11 may begin independently.
- Notes: the portable default remains Codex `workspace-write` with tool network
  disabled. The container path is an opt-in conformance fallback for hosts whose
  kernel policy prevents nested Codex sandbox construction; it is not a required
  runtime dependency for Crewfold.
