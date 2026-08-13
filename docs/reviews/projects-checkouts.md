# M3 milestone review — Projects, repositories, and checkouts

## Identity

- Milestone: `M3 — Projects, repositories, and checkouts`
- Review status: `passed`
- Implementation commits: `2be19905ccd003b045b3af20a452177ac2d5f127`,
  `76f7dcef6464917eacec5921cc4d0368a2e66cf1`
- Reviewer: Crewfold local implementation gate
- Date: `2026-08-12`

## Demonstrable outcome

- User-visible capability: register a project from any existing local Git
  checkout, add adjacent standalone clones or linked worktrees as distinct
  checkouts, list stored locations, refresh branch/HEAD/dirty state, and retain a
  moved location as unavailable under its durable identity.
- Acceptance scenario path: `test/scenarios/projects-checkouts/run.sh`
- Exact command: `./scripts/check.sh`
- Expected result: formatting, vet, unit, schema, protocol, race, and M0–M3
  black-box checks pass; the source scenario prints `Projects and checkouts acceptance: PASS`; no source
  content, daemon, socket, or temporary fixture remains.
- Observed result: passed on Linux/amd64 with Go 1.26.5 and the installed Git CLI.
  Three adjacent standalone clones and one linked worktree produced four checkout
  IDs and one repository ID.

## Test evidence

| Suite | Command | Result | Evidence |
| --- | --- | --- | --- |
| Formatting/static | `./scripts/check.sh` | passed | `gofmt` clean and `go vet ./...` passed |
| Unit/store | `go test ./...` via check script | passed | Domain records, CLI, store, daemon, Git observer, protocol, and prior packages |
| Race | `go test -race ./...` via check script | passed | New Git/store/API paths and prior crash harness are race-clean |
| Git observer | `go test ./internal/gitstate` | passed | Adjacent clones, linked worktree, path normalization, dirty state, absent Git, malformed output, bounded command allowlist, content digest, and hostile fsmonitor suppression |
| Store/schema | `go test ./internal/store` | passed | Current-baseline integrity, atomic registration, shared repository identity, duplicate path, unavailable retention, events, and idempotency |
| Protocol | `go test ./protocol` | passed | Unique valid schema IDs/references and M3 result-constant agreement |
| Component | `go test ./internal/daemon` | passed | Real Git/Unix socket/SQLite flow, standalone and linked checkouts, dirty/moved refresh, restart, non-repository rejection, and fake Git failures |
| Black-box acceptance | Projects and checkouts scenario via check script | passed | Public CLI registers four locations, proves content digest unchanged, refreshes state, restarts, and restores the same checkout list |
| Earlier milestones | Build, daemon, and persistence scenarios via check script | passed | All three capability-named acceptance messages were observed |
| Clean module cache | `GOMODCACHE=<empty> GOPROXY=off go test -count=1 ./...` | passed | Vendored build and test require no downloaded module |
| CGO independence | `CGO_ENABLED=0 GOPROXY=off go test -count=1 ./...` | passed | M3 retains the portable SQLite/build boundary |
| Repetition | `go test -count=10 ./internal/gitstate ./internal/store ./internal/daemon` | passed | Observer, persistence, transport, and restart paths passed repeatedly |
| Scenario repetition | Five consecutive projects/checkouts scenario runs | passed | No lifecycle, Git observation, persistence, or cleanup flake |
| Live conformance | N/A | M3 invokes local fixture Git only | No model, runtime, credential, remote, or paid call |

## Failure proof

- Injected failure: an absent Git executable and malformed Git output; a store
  interruption after source projections; a non-repository directory; a moved
  registered checkout.
- Injection seam/barrier: replaceable Git command runner/inspector,
  `after_projection` transaction hook, real public `project.add`, and a renamed
  temporary fixture directory.
- Expected diagnosis and recovery: Git failures return scoped stable codes and
  create no project/repository/checkout/event/idempotency fragment; transaction
  failure rolls all source projections back; a moved checkout remains registered
  but becomes unavailable; later list/restart does not need the path to exist.
- Observed diagnosis and recovery: `git_unavailable`, `git_output_invalid`,
  `not_git_repository`, and `checkout_unavailable` were observed as specified.
  Failed registration left only the pre-existing workspace event. The moved
  checkout retained its checkout ID, repository ID, path, and last-known Git
  state at revision 2.
- Operation/event IDs: successful registration returns project, repository, and
  checkout IDs plus the final event cursor. Request IDs are event correlation IDs.

Duplicate normalized paths return `checkout_already_registered` without an event.
If a registered path later resolves to another history, refresh records
`repository_identity_changed` rather than silently relinking the checkout.

## Persistence and recovery

- Durable state exercised: `projects`,
  `repositories`, `project_repositories`, and `checkouts`; successful mutations
  add `project.registered`, `repository.registered`, `checkout.registered`, and
  changed refreshes add `checkout.git_observed`.
- Restart/crash points tested: graceful restart after four registrations and a
  missing/dirty refresh; injected transaction rollback after all source
  projections and before events/idempotency.
- Reconciliation outcome: four checkout identities and their one shared
  repository identity survive restart. Missing paths remain unavailable rather
  than being deleted.
- Current-baseline tests cover representative workspace/event/idempotency and
  project/repository/checkout rows without fabricated state.
- Backup/restore impact: an online backup must include the live
  WAL state. There is no M3 backup command.

## Security and autonomy

- New actions/capabilities: run bounded local Git observations and mutate only
  Crewfold's project/repository/checkout projections and journal.
- Allowed/denied scope: read existing work-tree identity/status; reject missing,
  non-directory, non-repository, malformed, duplicate, and changed-history cases.
  M3 cannot create/delete a checkout, branch, commit, remote, or source file.
- Secret/redaction impact: normalized local paths, branch names, commit IDs, and
  compact diagnostics become durable local data. Git stderr is returned for local
  diagnosis; arbitrary request bodies remain absent from logs. No credential is
  requested or stored.
- External side effects: installed Git subprocesses only. Commands are restricted
  to `rev-parse`, `rev-list`, and porcelain `status`; optional locks, fsmonitor
  hooks, untracked-cache writes, maintenance, GC, and terminal prompts are
  disabled. A control test proves a configured fsmonitor hook would execute under
  ordinary status but is not invoked by Crewfold.
- Human approval boundary: the owner explicitly selects every registered path and
  invokes refresh. There is no autonomous watcher, runtime, agent, or write action.

The fixture's source and `.git` file contents/modes are hashed before and after
registration/inspection. The digest remains identical until the test deliberately
creates a dirty file and renames one fixture checkout.

## Compatibility

- API/schema changes: additive protocol-v1 methods `project.add`,
  `project.inspect`, `checkout.add`, and `checkout.list`; eight parameter/result
  schemas and three domain schemas.
- Storage evidence: fresh-baseline creation validates the exact tables, indexes,
  and canonical identity rules.
- Adapter/runtime compatibility changes: none; no runtime/provider adapter exists.
- Earlier milestone scenarios rerun: M0, M1, and M2 pass. M2's storage-health
  assertion now intentionally accepts the binary's latest schema rather than
  freezing the historical version number.
- Restore impact: restore targets a new data directory and must pass current
  canonical integrity before serving.

## Known limitations and deferrals

- Git provides no repository UUID. M3 fingerprints object format plus sorted
  reachable root commits. Shallow clones, added orphan histories, or rewritten
  roots may require a future explicit identity-reconciliation workflow.
- Refresh is synchronous and sequential across a project's checkouts. A bounded
  background watcher and large-project performance evidence are later work.
- A moved path is diagnosed but not automatically rediscovered or relinked.
- Empty repositories and bare repositories are not registerable checkouts because
  M3 requires a work tree and a commit at HEAD.
- The installed Git executable is required for registration and refresh; stored
  `checkout.list` remains available without Git or the filesystem path.
- Write modes are durable policy data only. Enforcement begins when tasks/runs can
  claim or launch against a checkout.
- M3 does not create Git worktrees/clones or mutate source. Agents, tasks,
  runtimes, messages, claims, knowledge, Herdr, MCP, and provider SDKs remain
  deferred.

## Repository hygiene

- Working tree clean after implementation acceptance: yes.
- No leaked processes/sockets/temp directories: yes.
- No paid/network call in default tests: yes.
- Documentation matches behavior: yes, including adjacent standalone clones as
  first-class checkouts rather than an assumption of linked worktrees.
- No upstream Git remote created: yes.

## Decision

- Exit gate satisfied: `yes`.
- Waivers: none.
- Next milestone entry criteria met: `yes`.
- Next milestone: `M4 — Durable agents and tasks`.
