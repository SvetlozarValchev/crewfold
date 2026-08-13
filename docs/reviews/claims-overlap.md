# Milestone review — claims and deterministic overlap detection

## Identity

- Milestone: `M12 — Claims and deterministic overlap detection`
- Review status: `passed`
- Implementation commit: `f756d7c427a82f3661997ccacdbe94ab1d085b36`
- Reviewer: `automated deterministic acceptance and repository gate`
- Date: `2026-08-12`

## Demonstrable outcome

- User-visible capability: declare a leased path/component/operation scope for a
  task, inspect a persisted overlap with a concrete witness and deterministic
  explanation, apply notify/deny/pause/request-resolution policy, rescan claimed
  checkouts, and inspect out-of-scope dirty-path drift.
- Acceptance scenario path: `test/scenarios/claims-overlap/run.sh`
- Exact focused command: `test/scenarios/claims-overlap/run.sh`
- Full command: `./scripts/check.sh`
- Expected result: the focused scenario prints
  `Claims, overlap, and drift acceptance: PASS`; all prior unit, race, and
  black-box gates continue to pass without provider credentials, inference, or
  network access.
- Captured structured result: schema-v8 claim/overlap/drift projections; public
  `claim.*`, `overlap.*`, and `drift.list` local API/CLI JSON; `claim.added`,
  `overlap.opened`, and `claim.drift_opened` events.

## Deterministic policy contract

Scope intersection is exact for the implemented declaration language:

- component/operation targets intersect only on equal labels;
- path targets support repository-relative literals, segment-local `*`/`?`, and
  whole-segment `**`;
- path-pattern intersection explores the product automaton and returns a concrete
  path accepted by both patterns; and
- bounded exhaustive tests compare its answer and witness with concrete-path
  enumeration.

Severity and response are independent matrices:

| Mode pair | Severity |
| --- | --- |
| either advisory | low |
| shared/shared | medium |
| exclusive/shared | high |
| exclusive/exclusive | critical |

Policy precedence is `deny_new`, `pause_scheduling`, `request_resolution`, then
`notify`. Denial leaves no new claim or overlap. Pause creates durable holds for
both tasks; `run.start` returns `scheduling_paused` until release or expiry resolves
the overlap. It does not stop a run already in progress.

No semantic search, embedding, model output, terminal text, or Git worktree
assumption participates in these decisions.

## Test evidence

| Suite | Command | Result | Evidence |
| --- | --- | --- | --- |
| Domain/glob | `./scripts/go.sh test ./internal/domain` | passed | normalization, exact witness, conflict matrix, concrete matching, bounded exhaustive comparison |
| Git observer | `./scripts/go.sh test ./internal/gitstate` | passed | porcelain-v2 dirty paths, spaces, rename destination, adjacent clones/worktrees, read-only commands |
| Store/schema | `./scripts/go.sh test ./internal/store` | passed | current-baseline claim/overlap integrity, deny, pause, release, expiry, restart, drift, gap, checkout attribution, injected rollback |
| Daemon/CLI/protocol | `./scripts/go.sh test ./internal/daemon ./internal/cli ./protocol` | passed | watcher lifecycle, public commands, JSON schemas, result-ID contract |
| Black-box acceptance | `test/scenarios/claims-overlap/run.sh` | passed | three concrete checkouts, cross-checkout overlap, atomic denial, shared warning, stopped-watcher drift, unchanged declaration |
| Full offline/race gate | `./scripts/check.sh` | passed | vet, all tests, race tests, and all thirteen black-box scenarios |

## Failure proof

- Injected transaction failure: the store hook interrupts `claim.add` after its
  primary projection write. The transaction rolls back claim, overlap, event, and
  idempotency state; table and event counts remain unchanged.
- Deterministic policy failure: a third task submits a conflicting `deny_new`
  claim. The public CLI returns stable code `claim_conflict`; listing proves only
  the original two claims exist.
- Watcher outage: the acceptance establishes a scan, stops the daemon, writes an
  out-of-scope file, and restarts. Startup finds `outside-contact.txt`; the drift
  records `observation_gap: true` because the watcher identity changed.
- Git parsing failures: malformed porcelain-v2 records become bounded
  `git_output_invalid` diagnostics rather than guessed paths.
- Operation/event identifiers: accepted mutations return claim/overlap IDs and
  event sequences; drift/overlap facts are visible through `events.list`.

## Persistence and recovery

- Durable state introduced: checkout dirty-path snapshots, work claims, overlaps,
  task coordination holds, claim drift, and last watcher scans.
- Restart points tested: controlled-clock store close/reopen after a claim lease
  expires; daemon stop/file mutation/restart; all earlier daemon and runtime
  restart cases remain in the full gate.
- Reconciliation: claim queries/creation and run scheduling expire elapsed claims,
  resolve their overlaps, and remove holds. A new daemon watcher preserves the
  last scan but marks its first subsequent out-of-scope observation as gapped.
- Baseline integrity: fresh storage initializes canonical empty dirty-path arrays,
  and no claims are fabricated before an owner creates them.
- Backup impact: a database backup must include claim, overlap, hold,
  drift, and watcher-scan tables. No provider-private state is added.

## Security and autonomy

- New actions: bounded read-only Git observation and local scheduling denial for
  an explicitly configured pause policy.
- Git commands retain optional-lock, filesystem-monitor, untracked-cache,
  maintenance, and terminal-prompt suppression. The watcher does not edit files,
  branches, indexes, worktrees, remotes, or commits.
- Paths are repository-relative and reject absolute paths, parent segments,
  backslashes, character classes/braces, malformed `**`, invalid UTF-8, and
  control characters.
- A shared checkout returns an explicit warning: claims coordinate intent but do
  not supply filesystem or operating-system isolation.
- No claim response silently reassigns tasks, creates dependencies, stops active
  runs, resolves code, pushes, merges, or contacts a person.
- No secret, credential, provider transcript, model call, paid service, or network
  boundary is introduced.

## Current contract

- Storage contract: the current baseline includes strict claim/overlap tables,
  indexes, and required checkout dirty-path arrays.
- Domain/API contract: `dirty_paths` is required in checkout JSON; claim,
  overlap, scan, and drift records/methods are additive local API v1 contracts
  with published JSON Schemas.
- Scheduler change: `run.start` adds a deterministic open-hold check after lease
  reconciliation and before placement.
- Source-layout compatibility: adjacent standalone clones and linked worktrees
  remain distinct checkout IDs. Repository identity neither merges their dirty
  paths nor determines drift attribution.
- Earlier milestone scenarios: M0 through M11 all passed unchanged in the final
  `scripts/check.sh` run. Recorded Codex and Claude gates made no model call.

## Known limitations and deferrals

- Claim renewal is not exposed yet; release and expiry are implemented.
- Path claims coordinate declared intent at project scope and attribute drift to
  one checkout. They are not locks, sandboxes, merge queues, or proof of which
  process wrote a file.
- Dirty status cannot identify an exact write timestamp or writer. Baselines avoid
  attributing already-dirty paths; watcher identity exposes restart gaps, but no
  system can reconstruct every change made during an unobserved interval.
- The watcher covers active path-claim checkouts. Component/operation claims have
  no source-drift inference, and semantic similarity cannot create a conflict.
- Current run context packets explicitly exclude live claim/overlap snapshots;
  owner CLI/API inspection works, while agent-scoped claim MCP tools remain
  deferred.
- `request_resolution` marks the overlap as needing resolution but does not run a
  meeting. Frozen-input, two-/three-participant consolidation and authorized
  dependency/claim mutation belong to M13.
- One local SQLite connection and personal-node scale remain intentional. The
  100-role load/recovery target and organization-wide federation are later gates.

## Repository hygiene

- Working tree clean after implementation commit and acceptance: yes before this
  review record was added.
- No leaked processes, sockets, or temporary directories: yes; focused and full
  scenarios own and remove their resources.
- No paid/network call in default tests: yes.
- Public upstream created: no.
- Files/variables named after the milestone: no; names describe claims, overlaps,
  drift, watcher scans, and coordination holds.
- Documentation matches behavior: yes.

## Decision

- Exit gate satisfied: `yes` — two fixture tasks deliberately overlap across
  separate checkout directories, and Crewfold warns or blocks exactly as declared
  by deterministic policy; stopped-watcher drift is durable and explicit.
- Waivers and accepting authority: none.
- Next milestone entry criteria met: `yes`; M13 may begin.
- Next question: can a bounded two- or three-agent procedure turn this durable
  conflict into an authorized, explainable task/claim change without reading
  terminal transcripts?
