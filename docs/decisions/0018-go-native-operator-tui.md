# ADR-0018: Go-native operator TUI over the canonical local API

- Status: accepted
- Date: 2026-08-14

## Context

Crewfold's local API and CLI expose the durable control plane, but operating a
project currently requires remembering and combining many commands. Herdr keeps
provider processes visible and attachable; it is not a management dashboard and
does not own Crewfold tasks, accepted outcomes, approvals, evidence, or policy.
An operator needs one coherent view of those facts without making terminal pane
contents or a second client-side projection authoritative.

The dashboard must remain trustworthy through daemon restarts, large event
bursts, small terminals, hostile provider-authored text, and ambiguous mutation
responses. It must preserve the owner approval boundary and the exact M18
briefing representation rather than replacing structured facts with a parallel
narrative or approximate count.

## Decision

### One Go-native terminal client

`crewfold ui` is a full-screen terminal client in the existing Go binary. It
directly uses Bubble Tea v2.0.8, Bubbles v2.1.1, and Lip Gloss v2.0.6. These are
implementation dependencies, not interfaces: there is no framework adapter,
alternate renderer, Bubble Tea v1 compatibility path, browser fallback, or
placeholder UI implementation.

The current invocation is:

```text
crewfold ui [--socket PATH] [--workspace ID_OR_NAME]
            [--project ID_OR_NAME] [--color auto|never]
```

The interactive command rejects `--output`; structured automation continues to
use the ordinary CLI and local API.

One Bubble Tea model owns connection state `connecting|syncing|live|
reconnecting|fatal`, a route stack of at most 16 entries, focus
`navigation|records|detail|modal`, and stable-ID selection. `View` is a pure
render of model state and performs no I/O. A generation token discards late
responses from superseded loads.

The screens are exactly Overview, Briefing, Work, Decisions, Checks,
Coordination, and Activity. The Briefing screen consumes M18 claim IDs, ordered
claims, sources, evidence classifications, omission counts, canonical hash, and
event cursor unchanged. Inspecting a briefing claim reads its canonical M18
explanation on demand and presents its exact claim, provenance, and current-source
diagnoses; it does not synthesize a client-side explanation. Other screens use
canonical local-API reads; the UI does not read SQLite, terminal buffers, or
provider transcripts.

### Deterministic responsive layout and keyboard operation

The layout is determined solely by the current terminal size:

- width at least 120 **and** height at least 32: navigation, records, and detail
  panes;
- otherwise, width at least 80 **and** height at least 24: records and detail
  with navigation as an overlay;
- otherwise, width at least 60 **and** height at least 18: one routed pane; and
- width below 60 **or** height below 18: one stable too-small message and no
  clipped pseudo-dashboard.

The state and render suites exercise the exact boundaries `60x18`, `79x23`,
`80x24`, `119x31`, and `120x32`, plus a large terminal. Selection is by durable
entity or claim ID, never list offset, so sorting, refresh, and reconnect retain
the same record when it remains visible and choose the documented nearest
record when it disappears.

Keyboard operation is complete without a mouse: `Tab`/`Shift+Tab`, arrows or
`j`/`k`, `PgUp`/`PgDn`, `g`/`G`, `Enter` to inspect only, `Esc` to go back or
cancel, `/` to filter, `a` to open available actions, `r` to refresh, `?` for
help, and `q` to quit outside a modal. `Ctrl+C` first cancels an open modal or
in-flight noncommitted interaction, then quits.

### Canonical synchronization with an applied cursor

Bootstrap first captures a workspace high-water and then loads bounded canonical
reads at that cut. Journal event envelopes are invalidation signals only; their
payloads never become displayed canonical entity state. The model distinguishes
candidate, applied, and high-water cursors. It advances the applied cursor only
after all reads invalidated through that candidate have refreshed successfully.
After the section batch completes, one final bounded event-head read fences the
batch. If the high-water advanced during any section read, the client discards
that mixed candidate and refreshes through the newer cursor instead of briefly
presenting it as live.

Live operation polls every 500 milliseconds with at most one poll in flight. It
processes at most ten pages of 1,000 events before yielding, retains at most 200
activity rows and 100 notifications, and uses reconnect delays of 250
milliseconds, 500 milliseconds, one second, two seconds, and four seconds,
capped at five seconds. Duplicate envelopes do not create duplicate
notifications. Malformed, nonmonotonic, oversized, or unknown envelopes fail
closed; an unknown canonical event invalidates all loaded views. If the daemon's
high-water is behind the applied cursor, the client discards its cache and runs a
full bootstrap rather than attempting a compatibility replay.

During reconnect the last successfully applied cache remains visibly stale and
all mutations are disabled. Missing workspace and protocol mismatch are fatal
diagnoses. Daemon loss during bootstrap, polling, canonical refresh, or an action
has an explicit recovery state and never silently advances the cursor.

All UI reads use bounded current local-API methods. Name selection resolves first
through pure `workspace.show`, `project.show`, or `agent.show`; every subsequent
operator wire request carries canonical IDs. Project resolution never uses the
Git-refreshing inspection operation, so bootstrap and manual refresh cannot append
events. Ordinary list pages default
to 50 and permit at most 200 records; opaque, scope-and-filter-bound cursors are
at most 256 bytes. One screen load follows at most three pages and therefore at
most 600 records. Event pages contain at most 1,000 envelopes. Local-API
responses are limited to 16 MiB. At most four non-event reads run concurrently;
each section result carries its load generation, stale generations are discarded,
and the applied cursor waits for every section invalidated through the candidate.
Ordinary canonical reads time out after five seconds, event polls after two
seconds, and both the bounded briefing read and owner actions after 15 seconds.
An on-demand briefing explanation is a generation-bound briefing read: a late
response from another workspace, briefing, claim, or generation is discarded,
and returned claim/provenance must still match the selected canonical claim.

### Owner-visible and replay-safe interventions

Navigation, filtering, refresh, inspection, explanation, and help are read-only
and append no Crewfold event. Pressing `Enter` only inspects. `a` opens a closed
set of currently valid actions; no action occurs until a review modal shows the
exact target ID, expected revision, and consequence and the owner presses
`Ctrl+Enter`.

An approval review first resolves the approval's immutable supervisor action
through the canonical read API. The UI verifies the action ID and revision,
awaiting-approval status, approval linkage, workspace, and scope against the
approval request. It then shows the exact condition, typed response, reasons,
scope/target IDs, action revision, and approval revision. Confirmation is
disabled if that evidence is missing or stale; a generic grant-or-deny label is
not informed owner consent.

Every mutation uses the same typed local-API method, policy evaluation, expected
revision, and idempotency semantics as its CLI equivalent. A lost response is
retried only with the same request and key. Replay returns the one committed
result; conflict, denial, approval-required, and degraded connection remain
distinct diagnoses. Agent role strings and launch-profile purpose strings are
display metadata only and never affect authority, action availability, urgency,
or ordering.

### Treat displayed and attached process data as untrusted

Before layout or output, all external strings are normalized to valid UTF-8 and
sanitized of ESC/OSC sequences, C0 and C1 controls, and bidirectional formatting
controls. Only newlines intentionally introduced by a view are retained. Status,
severity, focus, and selection always have textual labels. `--color never` and
`NO_COLOR` remove styling without removing meaning.

The dashboard never displays attach environment values, capability material, or
opaque provider/runtime handles. Attaching resolves the selected run through the
canonical `RunAttach` result and executes its exact executable and argument
vector with `tea.ExecProcess`; it does not invoke a shell. The TUI suspends,
restores terminal state, refreshes size, drains queued invalidations, and resumes
after either a zero or nonzero attached-process exit. M19 exposes ordinary attach
only. Run takeover is deferred until a later milestone defines it as a real
mutation with an expected revision, idempotency key, and durable receipt; there is
no dormant or unsafe TUI takeover path.

## Consequences

- The normal owner workflow has one dashboard while the CLI remains the exact
  scriptable and diagnostic surface.
- The daemon and local API remain the only authority; reconnect cannot turn
  journal payloads or cached screen rows into facts.
- M18 management compression is presented without changing a claim, trust
  classification, omission count, hash, or cursor.
- Small terminals, monochrome terminals, and provider-controlled text remain
  usable without hidden status or terminal-control injection.
- Actions stay deliberate, attributable, optimistic-concurrency-safe, and
  idempotent under ambiguous transport failure.
- Herdr continues to own terminal process hosting and attachment while Crewfold
  owns durable management state and the operator workflow.

## Rejected alternatives

- A sequence of setup and polling commands as the primary interface: this does
  not provide one persistent management context.
- A browser, Electron client, or separate frontend service: it adds another
  deployment unit before the local operator workflow is proven.
- Query SQLite directly from the TUI: it bypasses protocol, policy, bounded-read,
  and restart contracts.
- Build screen state from event payloads: events invalidate canonical records but
  are not a second read model.
- Infer actions from role or purpose labels: descriptive metadata grants no
  authority.
- Make `Enter` perform a default mutation or accept with a single keystroke: an
  intervention must display its exact target, revision, and consequence.
- Render terminal/provider text verbatim: escape and bidirectional controls can
  corrupt the display or misrepresent operator intent.
- Reimplement a terminal multiplexer: Herdr already owns the live terminal and
  `RunAttach` supplies the exact safe handoff.
