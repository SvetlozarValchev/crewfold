# Milestone review — operator TUI

## Identity

- Milestone: `M19 — Operator TUI`
- Review status: `passed`
- Implementation commit: `12cc6e147a6d5ffc92c15708656fecdcaec3d98c`
- Reviewer: `automated acceptance and independent adversarial review`
- Date: `2026-08-14`

## Demonstrable outcome

- User-visible capability: `crewfold ui` launches one Go-native, keyboard-driven
  dashboard over the current owner socket. It renders Overview, Briefing, Work,
  Decisions, Checks, Coordination, and Activity from bounded canonical reads,
  retains honest stale state through reconnect, attaches to an exact Herdr run,
  and submits mutations only after an exact review and `Ctrl+Enter`.
- Acceptance scenario path: `test/scenarios/operator-tui/run.sh`.
- Exact command: `./test/scenarios/operator-tui/run.sh`.
- Observed result: `Provider-free operator TUI acceptance: PASS`. The real built
  binary ran in a 180x40 monochrome pseudo-terminal, displayed the complete M18
  briefing cut and SHA-256, attached and returned, retained the selected run
  across daemon loss/restart, appended no event for inspection or attach, and
  appended exactly one `run.resumed` event for the reviewed intervention.

## Acceptance matrix

The milestone cannot pass until all rows below have executable assertions.

| Boundary | Required proof | Status |
| --- | --- | --- |
| Model/render | Empty, normal, degraded, capped, and large fixtures; pure `View`; deterministic state reduction | passed |
| Layout | Exact `60x18`, `79x23`, `80x24`, `119x31`, and `120x32` boundaries plus a large terminal and resize transitions | passed |
| Selection/routes | Stable-ID retention across reorder, removal, refresh, and reconnect; route-depth 16; focus/modal transitions; bounded inbox/timeline/explanation caches with duplicate-owner and in-flight back-navigation safety | passed |
| Keyboard | Complete documented key map; `Enter` inspect-only; `q` outside modal; first `Ctrl+C` cancels | passed |
| Briefing parity | Deep equality for ordered claims, sources, evidence classification, omissions, hash, and cursor; on-demand explanation preserves exact claim/provenance/current-source diagnoses | passed |
| Drill-down | Every urgent aggregate opens the exact complete canonical record set that produced it | passed |
| Accessibility | `NO_COLOR` and `--color never`; status/severity/focus text labels; no color-only state | passed |
| Terminal safety | Invalid UTF-8, ESC/OSC, C0/C1, bidi controls, long text, and intentional-newline handling | passed |
| Bootstrap/catch-up | Initial and final high-water fence around bootstrap/refresh; event-during-read mixed batch rejection; 1,000 boundary, more than 10,000 events with yield, bounded activity/notifications; generation-bound sections and applied cursor waiting for all invalidated sections | passed |
| Event integrity | Duplicate, malformed, nonmonotonic, unknown, oversized, wrong-scope, and filter/cursor mismatch behavior; unknown kinds are definitive across forward and timeline reads | passed |
| Reconnect | Kill during bootstrap, poll, refresh, and action; exact backoff; stale cache; selection retention; no duplicate notification | passed |
| Rewind/fatal | Daemon rewind forces full bootstrap; missing workspace and protocol mismatch fail explicitly | passed |
| I/O bounds | One event request and at most four non-event requests in flight; 500 ms polling; read/page/cursor/response limits; stable totals, unique identities, advancing cursors, and hanging request timeouts | passed |
| Read-only proof | Project ID/name resolution uses a pure bounded read; bootstrap, navigation, filter, inspect, explain, refresh, help, resize, reconnect, and attach append no fact event | passed |
| Mutation review | Exact target/revision/consequence; displayed stop grace equals the frozen wire value; approval notes are canonical before freeze and byte-identical on replay; approval review resolves and validates immutable supervisor condition/response/reasons/scope and both revisions; `Ctrl+Enter` only; one event and idempotent replay | passed |
| Mutation failures | Lost response, revision conflict, denial, approval-required, action timeout, degraded connection, and schema-valid but method-wrong mutation outcomes | passed |
| Attach | Exact argv without shell; no env/handle display; suspend, resize, nonzero exit, reconnect, drain, and resume | passed |
| Authority | Arbitrary `AgentDefinition.Role` and `LaunchProfile.Purpose` do not affect order, urgency, actions, or permission | passed |
| Concurrency | Reducer/message tests pass under the Go race detector | passed |
| Real program | Noninteractive Bubble Tea program uses controlled input, output, and window size | passed |
| Public scenario | Provider-free `test/scenarios/operator-tui/run.sh` exercises the built binary and daemon restart | passed |

## Test evidence

| Suite | Command | Result | Evidence |
| --- | --- | --- | --- |
| All Go packages | `./scripts/go.sh test ./... -count=1` | passed | Current tree across CLI, daemon, domain, execution, Git, Herdr, local API, store, TUI, and protocol |
| Operator/API/storage packages | `./scripts/go.sh test ./internal/tui ./internal/localapi ./internal/store ./internal/daemon ./internal/cli ./protocol -count=1` | passed | Reducer/render/transport, strict client, frozen event pages, pure operator reads, lease reconciliation, CLI parsing, and schema contracts |
| Operator race gate | `./scripts/go.sh test -race ./internal/tui ./internal/localapi -count=1` | passed | Concurrent reads, polling, refresh, reconnect, action, attach, and stale-message reducers are race-clean |
| Static analysis | `./scripts/go.sh vet ./internal/tui ./internal/localapi ./internal/store ./internal/daemon ./internal/cli ./protocol` | passed | Current operator implementation and its canonical dependencies |
| Public black box | `./test/scenarios/operator-tui/run.sh` | passed | Real binary/daemon/PTTY, briefing cut/hash, read-only inspection and attach, daemon kill/restart, retained selection, and exactly one reviewed resume |
| Scenario/static hygiene | `sh -n test/scenarios/operator-tui/run.sh && git diff --check` | passed | Portable shell parses and the current diff has no whitespace errors |
| Complete repository gate | `./scripts/check.sh` | passed | Generated-query consistency, formatting, full vet/test/race, and every provider-free/recorded black-box scenario, including the wired operator TUI scenario |

## Failure proof

- Injected failures: event commits during canonical reads; duplicate,
  nonmonotonic, malformed, unknown-kind, wrong-workspace, cyclic-cursor, and
  inconsistent-total pages; ordinary collection cursor stalls/cycles, duplicate
  identities, and terminal under/over-counts; more than ten event pages; sparse
  collections beyond three pages; unknown/duplicate request members, explicit
  invalid zero values, malformed action targets, oversized scopes, invalid
  filters, oversized and hanging responses; daemon loss at asynchronous
  boundaries; journal rewind; stale section/inbox/explanation/attach completions;
  wrong-run attach results; invalid executable/environment data; canceled attach;
  lost mutation responses; swapped resume/stop and allow/deny results; stale
  response revisions, notes, or linkage; denials; and scope changes while an
  ambiguous request is frozen. Route-cache traces evict and replace frames at
  depth, churn across more agents than the route bound, reopen the same task or
  briefing claim with and without a prior cache, leave while the second read is
  in flight, and deliver its completion late.
- Expected diagnosis and recovery: transport violations discard the whole result;
  unknown events and rewinds invalidate cached truth; transient loss retains an
  explicitly stale cache and disables mutations; stale generations cannot replace
  current state; ambiguous mutations retain only their exact frozen request and
  key; a canonical scope change invalidates that request; canceled or mismatched
  attach never launches a child.
- Observed diagnosis and recovery: all focused reducer, concrete-socket, store,
  daemon, client, race, and public-scenario assertions matched. The pseudo-terminal
  scenario visibly entered reconnecting/stale state and returned live after the
  same daemon restarted without relaunching the UI.

The strict client resolves documented workspace, project, and agent names with
bounded `show` reads, then sends canonical IDs on operator wires. Raw operator
requests containing names are rejected. Inbox reads selected through either a
name or canonical ID return one canonical agent ID and bind every delivery,
message, and workspace to it. Forward and reverse event clients reject a
structurally valid future event kind with the definitive
`unsupported_operator_event` code; feeding that concrete transport result to the
reducer invalidates every cached fact.

## Persistence and recovery

The TUI introduces no durable authority or client-owned projection. Bootstrap
captures a high-water, loads bounded canonical sections, then validates a final
event-head fence before applying. Event envelopes only invalidate; the applied
cursor advances after the affected canonical reads complete. Tests prove exact
idempotent replay after a lost response, full cache invalidation on rewind or an
unsupported event kind, and stable-ID selection across an ordinary reconnect.

An implicit sole workspace is pinned to its canonical ID for the UI session.
Creating a second workspace later therefore cannot reopen a chooser over a frozen
ambiguous mutation review; the next resolution uses `workspace.show`. A genuine
workspace/project scope change invalidates the frozen request and requires the
operator to inspect canonical history in the original scope.

## Security and autonomy

Executable tests prove terminal-string sanitization and a strict window-derived
render bound, hidden attach environment and opaque handles, argv-preserving
shell-free attach, read-only navigation, an explicit scroll-to-review plus
`Ctrl+Enter` owner boundary, and inert role/purpose metadata. Approval review
binds the immutable supervisor action, approval revision, expected action
revision, condition, response, reasons, scope, consequence, and idempotency key.
The stop review displays the exact frozen 5,000 ms grace value sent on the raw
request. A whitespace-padded approval note is canonicalized before confirmation;
the visible frozen note, first request, and ambiguous replay are byte-identical.

## Current contract and external conformance

- API/schema changes: current-only bounded workspace/project/meeting and entity
  list reads, forward workspace event pages, reverse entity timelines, slim run
  summaries, opaque scope-bound cursors, and strict result discriminators
- TUI dependencies: Bubble Tea v2.0.8, Bubbles v2.1.1, Lip Gloss v2.0.6
- Earlier milestone scenarios rerun: the complete check script passed every
  earlier provider-free and recorded-provider scenario in the current tree
- Current-baseline integrity: focused store, daemon, local API, CLI, and protocol
  suites passed against the single current schema

## Known limitations and deferrals

Browser UI, remote operation, mouse-only operation, client-side narrative
generation, historical-cursor browsing, terminal multiplexing, and run takeover
from the TUI are absent, not dormant compatibility paths. TUI takeover requires
a future explicit mutation contract with expected revision, idempotency key, and
durable receipt; M19 exposes ordinary attach only. M20 scale/recovery hardening
and M21 packaging remain later milestones.

## Decision

- Exit gate satisfied: `yes`
- Waivers and accepting authority: `none`
- Next milestone entry criteria met: `yes`
- Notes: The focused package, full repository test/race/vet, shell, public
  scenario, and complete integration gates above passed on 2026-08-14. No
  waivers were needed, the independent final audits report zero unresolved HIGH
  or MEDIUM findings, and the accepted artifact contains no compatibility,
  deprecated, fallback, or dormant takeover path.
