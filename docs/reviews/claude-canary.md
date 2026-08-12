# Milestone review — Claude provider and provider-switch proof

## Identity

- Milestone: `M11 — Claude Code canary and provider-neutral proof`
- Review status: `passed`
- Implementation commit: `31c8fad1790b738e86516b119c6594293b9c99ba`
- Reviewer: `automated deterministic acceptance plus owner-approved gate policy`
- Date: `2026-08-12`

## Demonstrable outcome

- User-visible capability: probe an installed Claude Code CLI/auth session without
  inference, launch Claude through either direct or Herdr runtime with a required
  run-scoped MCP binding, and continue work sent by Codex through Crewfold-owned
  durable mail and curated context.
- Acceptance scenario path: `test/scenarios/claude-provider/run.sh`
- Exact offline command: `./scripts/check.sh`
- Expected result: all earlier suites pass and the final scenario reports
  `Claude provider and cross-provider handoff acceptance: PASS` without provider
  credentials, network access, or model usage.
- Captured structured result/artifact: provider doctor schema
  `urn:crewfold:schema:provider:claude-probe:v1`,
  `claude-provider:v1:<run-id>` binding, durable `message.sent`, immutable briefing
  inbox content, `run.tool_called`, `run.report_received`, accepted evidence,
  handoff, and terminal `completed` state.

## Provider conformance comparison

| Contract | Fake/fixture | Recorded Codex | Recorded Claude |
| --- | --- | --- | --- |
| Domain identity | same task, agent, checkout, run, placement, and revisions | same | same |
| Context authority | bounded scenario or immutable Crewfold briefing | immutable Crewfold briefing through scoped MCP | immutable Crewfold briefing through scoped MCP |
| Coordination | Crewfold task/report records | Crewfold MCP reports and durable mail | Crewfold MCP reports and durable mail |
| Runtime binding | fake/direct/Herdr fixture bindings | opaque Codex provider handle over direct or Herdr | opaque Claude provider handle over direct or Herdr |
| Completion authority | normalized report evaluated by core acceptance | Crewfold MCP proposal plus settled runtime | Crewfold MCP proposal plus settled runtime |
| Private provider state | none is domain authority | recorded thread ID stays in Codex diagnostics | recorded session ID stays in Claude diagnostics |
| Provider switch | provider-free messaging fixtures | sends provider-neutral durable handoff | receives handoff without Codex transcript state |

The task, message, context, report, acceptance, and scheduler contracts are reused.
Provider names appear only at adapter registration, selection, diagnostics, and
provider-specific CLI configuration; there is no Claude branch in domain policy.

## Test evidence

| Suite | Command | Result | Artifact/log |
| --- | --- | --- | --- |
| Unit | `./scripts/go.sh test ./internal/execution ./internal/cli ./internal/daemon ./protocol` | passed | probe, strict manifest, budget, credential denial, boundary normalization, CLI, daemon, schema |
| Black-box acceptance | `test/scenarios/claude-provider/run.sh` | passed | recorded Codex → durable mail → immutable Claude briefing → completion |
| Full offline/race gate | `./scripts/check.sh` | passed | vet, all tests, race tests, and twelve black-box scenarios |
| Installed no-model probe | built binary `doctor --provider claude --output json` | passed | installed Claude Code 2.1.220 version/help/auth only; account identity intentionally omitted |
| Container packaging probe | build local image; run version/auth and scoped-mount probes | passed | native binary starts as non-root; only copied auth; setuid/setgid scan empty; no inference |
| Gated live harness | `test/live/claude/run.sh` without flags and with only the first flag | passed | skips by default and refuses model usage without both acknowledgements |
| Live conformance | explicit N/A for milestone completion; documented opt-in harness retained | not run | optional release/upgrade evidence; no model call required by the deterministic gate |

## Failure proof

- Injected failures: recorded authentication is absent; provider major version is
  outside tested Claude Code 2.x; run capability paths are missing; recorded MCP
  startup fails; and a permission boundary denies tool use.
- Injection seam/barrier: bounded provider command runner, run-capability
  preparer, recorded CLI executable, and normalized provider observation.
- Expected diagnosis and recovery: `doctor` names authentication or compatibility;
  preparation names missing MCP access; MCP startup becomes a provider-boundary
  failure; permission output becomes blocked; terminal success without an MCP
  report never completes a task.
- Observed diagnosis: focused unit and black-box assertions pass with bounded
  provider-specific messages rather than a generic timeout.

## Persistence and recovery

- Durable state introduced or changed: no schema change. Existing run provider
  handle, messages, inbox delivery, context packet, tool-call audit, report,
  handoff, and event records are reused.
- Restart/crash points tested: every earlier daemon/runtime restart suite remains
  in the full gate; M11 introduces no provider-private durable saga.
- Reconciliation outcome: runtime drivers own process reconciliation. Claude
  completion is accepted only after the provider process settles and the existing
  MCP report has passed core evidence rules.
- Migration and backup impact: none.

## Security and autonomy

- New actions/capabilities: execute an allowlisted Claude CLI and the existing
  hidden Crewfold STDIO bridge in the assigned checkout.
- Launch boundary: one-shot stream JSON, no session persistence, strict inline MCP
  configuration, no normal user/project/local settings sources, disabled browser
  integration and slash commands, `dontAsk`, bounded tools, and a default `1.00`
  USD maximum.
- Native containment: sandbox enabled, unavailable sandbox is fatal, unsandboxed
  command retry disabled, built-in Read/Edit limited to project-relative paths,
  sandboxed reads of the host home denied except for the assigned checkout, and
  sensitive provider/cloud paths denied to Read and Edit. These fields follow Anthropic's current
  [sandbox](https://code.claude.com/docs/en/sandboxing),
  [permission-mode](https://code.claude.com/docs/en/permission-modes), and
  [CLI](https://code.claude.com/docs/en/cli-usage) contracts.
- Secret/redaction impact: the MCP configuration contains only socket and private
  capability-file paths; the bridge reads the token and injects it on the
  owner-only local socket. Tests assert that tokens do not reach launch arguments,
  terminal logs, or provider-switch context. The doctor reports auth method and
  API provider but never email, organization, or credential data.
- External containment: the optional live wrapper copies only Claude's credential
  file into disposable owner-private state and runs a local image as the current
  non-root UID with a read-only root, no Linux capabilities, and only the canary
  scope mounted. The daemon disables Claude's inner sandbox only when the operator
  separately asserts this outer boundary.
- Human approval boundary: `CREWFOLD_LIVE_CLAUDE=1` and
  `CREWFOLD_ALLOW_MODEL_CALLS=1` are both required. External-sandbox mode adds
  `CREWFOLD_EXTERNAL_CLAUDE_SANDBOX=1`. The prior Codex authorization does not
  authorize this call.

## Compatibility

- API/schema changes: additive provider doctor response schema and additive
  `doctor --provider claude` CLI form.
- Adapter/runtime changes: shared provider-command plumbing now serves Codex and
  Claude; both emit the existing `CommandSpec` and work with direct or Herdr.
- Supported provider range: the current adapter explicitly accepts Claude Code
  2.x and refuses an untested major. Capability flags are probed independently.
- Earlier milestone scenarios: all M0–M10 scenarios passed under
  `scripts/check.sh` after the Claude adapter and scope hardening were added.
- Upgrade/rollback impact: rollback removes Claude registration and doctor; SQLite
  needs no downgrade.

## Known limitations and deferrals

- Known limitation: the adapter is headless, one-shot, and ephemeral. Herdr can
  observe/attach to the terminal, but prompt delivery is not active-turn Claude
  steering.
- Explicitly deferred: native session persistence/resume, usage accounting beyond
  the launch budget ceiling, provider-aware mailbox wake, organization policy
  integration, claims, meetings, and canonical knowledge.
- The installed real-model canary remains available for release and provider-
  upgrade conformance. It is external, potentially metered evidence and is not a
  deterministic development gate. Offline success proves Crewfold's adapter,
  launch, and provider-switch contracts, not every future Claude release.

## Repository hygiene

- No leaked processes/sockets/temp directories: recorded acceptance cleanup
  passed.
- No paid/network call in default tests: yes.
- Public upstream created: no; the repository remains local-only.
- Documentation matches behavior: yes, including the optional live authorization
  barrier.

## Decision

- Exit gate satisfied: `yes` — deterministic Claude adapter and provider-switch
  contracts pass without inference.
- Waivers and accepting authority: none. The owner explicitly changed live
  provider conformance from a milestone prerequisite to an optional release and
  upgrade check on 2026-08-12.
- Next milestone entry criteria met: `yes`; M12 may begin.
