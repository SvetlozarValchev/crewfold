# Milestone review — owner-granted local checks

## Identity

- Milestone: `M17 — Local checks and reusable check-watch capability`
- Review status: `passed`
- Implementation commit: `91d4cb4d3f62f058d20c9b18bc2d408b988e78b8`
- Reviewer: `automated acceptance and independent adversarial review`
- Date: `2026-08-14`

## Demonstrable outcome

Crewfold can run an owner-defined local check in a separate durable lifecycle,
attach its exact mechanical result to one named task criterion, observe whether
that result remains fresh at the checkout HEAD, and route nonpassing or stale
facts to exact owner-selected recipients. An owner may grant any enabled agent a
bounded project-scoped check-watch capability; no role or profile-purpose string
participates in authorization.

Checks do not use agent runs, assignments, provider capacity, or task state as
their execution ledger. A request commits its job before launch, a starting
receipt freezes the exact definition/source/checkout/runtime specification before
external effect, restart reconciles the same direct-runtime operation ID, and one
immutable terminal result records `passed`, `failed`, `timed_out`,
`start_failed`, or `unknown`.

## Authority and evidence proof

- The current context packet may carry either one exact manager grant or one
  exact check-watch grant, never both. Ordinary runs use the same current packet
  without delegated authority.
- Every watcher operation revalidates the authenticated bound run, current exact
  grant and revision, enabled exact agent revision, project, operation, and
  allowed definition revision. `AgentDefinition.Role` and
  `LaunchProfile.Purpose` remain inert descriptive metadata.
- Check definitions freeze an absolute executable, ordered arguments, relative
  working directory, timeout, and output bound. Callers cannot inject shell text,
  environment, stdin, checkout, recipient, evidence class, or repair profile.
- A clean matching launch/terminal HEAD and exit zero produces fresh supporting
  mechanical evidence for exactly one requirement. Dirty or unavailable
  observations remain unknown; a later observed HEAD change is monotonically
  stale and never resurrects an old pass.
- Result outcome and freshness are independent. Failed fresh evidence
  contradicts; stale, unknown, timed-out, start-failed, and authority-denied
  results remain explicit and inconclusive where appropriate.
- Notifications resolve the exact current task owner or an exact owner-authored
  duty route and use honest `crewfold-check-worker` subsystem provenance. Missing
  or expired-unreserved ownership records an unroutable fact rather than guessing
  a recipient.
- Repair proposals require a current failed/fresh result and exact granted
  provenance. They are inert and disabled by default. Only one local-owner
  decision under a current bounded policy/profile can create a typed repair task
  and scheduling intent; replay, stale inputs, and objective-budget overflow
  create no duplicate or partial effect.

## Recovery, bounds, and security proof

- Direct-runtime launch/state/capture records are sealed, bounded, strict, and
  no-follow. A replay with another effective specification is rejected. Working
  directories are pinned without following a swapped symlink, and invalid UTF-8
  output is normalized before bounded redacted persistence.
- Check artifacts use private content-addressed storage with no-follow directory
  traversal, exact size/hash verification, definition-specific output limits, and
  no public filesystem path.
- Worker tests cover crashes before launch, after starting, after runtime binding,
  terminal persistence barriers, expired leases, grant/definition retirement,
  and daemon restart without a second child.
- The watch reconciler uses real Git observations, a sealed two-phase snapshot,
  a closed event classifier, exact replay-before-prepare, keyset paging, and
  restart-safe cursors. More than 100 scopes/results page without starvation;
  background no-ops produce no churn, while a public no-op retains one replayable
  receipt.
- Raw-SQL adversarial tests reject mutation, deletion, detachment, provenance
  forgery, evidence upgrades, freshness resurrection, route retargeting, orphan
  receipts, and duplicate repair effects.
- All check persistence uses named `sqlc` queries. The Store has one current
  baseline migration, no historical upgrade path, no old packet branch, and no
  obsolete or transitional shim.

## Public acceptance

The provider-free `test/scenarios/local-checks/run.sh` scenario uses several
agents with the same arbitrary role label. An ungranted run receives no check
tools and creates no check state. The exact grantee runs a real failing check,
reads bounded redacted logs, routes honest subsystem mail only to intended
recipients, proposes one inert repair, and proves the owner decision is the sole
cause of repair work. A later pass and HEAD change exercise verified-to-stale
truth, public watch replay, and exact-once daemon restart recovery.

The final exact tree passed:

- generated database parity, `gofmt`, `go vet ./...`, and `go test ./...`;
- `go test -race ./...` (`internal/store`: 460.999 seconds);
- every public black-box scenario in `scripts/check.sh`, including local checks,
  manager/supervisor, live context, Herdr, Codex, and Claude;
- an independent focused check/raw-SQL/repair/watch/fault-barrier audit with zero
  unresolved high- or medium-severity findings;
- a greenfield static audit finding one current packet schema, one current Store
  baseline, zero handwritten check-persistence calls, and no obsolete packet,
  migration-fixture, transitional, or dead-stub path.

## Deferrals

Remote hosted CI, dirty-worktree content fingerprints, untrusted-command
sandboxing/network denial, and any authority to merge, push, deploy, complete
tasks, accept policy, or choose integration order remain outside M17.
