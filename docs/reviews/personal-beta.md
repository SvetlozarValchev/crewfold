# Milestone review — personal beta

## Identity

- Milestone: `M20 — Personal-scale hardening and recovery`
- Review status: `passed`
- Implementation commit: `359522d8aa58a18a7d1151584a8f9bc48b4bfc56`
- Reviewer: `automated acceptance and independent adversarial review`
- Date: `2026-08-14`

## Demonstrable outcome

Crewfold now operates against one exact current database baseline, verifies the
complete canonical authority graph, creates an online quiescent backup, verifies
and restores that bundle without its source installation, and activates the
restored target only after explicit source-retirement confirmation. A restored
installation gets a fresh node identity and key; live runtime bindings,
capabilities, provider credentials, and operational runtime directories are not
cloned. Terminal direct and bounded Herdr logs survive through redacted immutable
artifacts instead.

The `personal-100` profile deterministically builds one workspace, ten projects,
100 arbitrary-role agent definitions, 1,000 tasks, and 100,000 known events with
80,000 attributed to one noisy project. It proves transactional admission,
bounded briefing fairness, responsive reads during write contention, full health,
recovery timing, RSS/disk limits, and authority neutrality without a paid provider
or network dependency.

- Public scenario: `test/scenarios/personal-beta/run.sh`
- Exact command: `./test/scenarios/personal-beta/run.sh`
- Observed result: `Provider-free personal beta recovery and load acceptance: PASS`

## Executable acceptance matrix

| ID | Accepted boundary | Status |
| --- | --- | --- |
| `M20-CUR-01` | One fresh-or-exact current baseline; canonical/control catalog identity and separate exact FTS diagnosis | passed |
| `M20-CUR-02` | Exhaustive table/queue/verifier ownership and one detected corruption in every semantic family | passed |
| `M20-CUR-03` | Fresh, captured, and restored canonical digest/event cursor equality | passed |
| `M20-BKP-01` | One online WAL cut with snapshot-derived immutable artifact closure | passed |
| `M20-BKP-02` | Every nonquiescent run/check/binding/job/wake/intent/action/approval class refuses publication | passed |
| `M20-BKP-03` | Kill, cancellation, and disk-full boundaries leave an absent or fully verifiable replayable target | passed |
| `M20-BKP-04` | Exact file grammar, modes, hashes, no-follow traversal, link/device/FIFO rejection, and closure checks | passed |
| `M20-BKP-05` | Verification and restore work after the source installation is removed | passed |
| `M20-RST-01` | Restore to a new inert target reproduces DB, artifacts, digest, and cursor | passed |
| `M20-RST-02` | No overwrite, merge, force, alias, reserved-path, or source/target-overlap path | passed |
| `M20-RST-03` | Pending restore refuses startup before identity, workers, listener, or runtime/provider calls | passed |
| `M20-RST-04` | Explicit activation creates distinct node authority and empty operational roots without changing domain history | passed |
| `M20-RST-05` | Tampered or newly actionable restored state fails before mutation or external effect | passed |
| `M20-RUN-01` | Public records hide handles; every control requires an allowed state and exact current-node binding | passed |
| `M20-RUN-02` | Bounded redacted terminal log artifacts survive restart and recovery with exact hashes | passed |
| `M20-RUN-03` | Lost runtime blocks capacity/backup until one owner retirement attestation resolves it | passed |
| `M20-HLT-01` | Full doctor is bounded, read-only, complete, and exact across canonical, queue, binding, artifact, and filesystem truth | passed |
| `M20-HLT-02` | Offline repair inspects a private copy and emits only bounded rebuild-or-restore guidance | passed |
| `M20-LOAD-01` | Exact `1/10/100/1,000/100,000/80,000` profile and repeatable role-neutral logical hash | passed |
| `M20-LOAD-02` | Complete measured workload meets fixed startup, latency, duration, RSS, and disk budgets | passed |
| `M20-LOAD-03` | Noisy-project pressure preserves all quiet-project urgent decisions within 128-claim/64-KiB bounds | passed |
| `M20-BP-01` | Manual/supervisor admission races never exceed `8/2/4/4/20` limits | passed |
| `M20-BP-02` | Saturated provider/starting lanes do not starve other provider, status, messaging, or reconciliation work | passed |
| `M20-BP-03` | Message wake is durable, signaled, asynchronous, bounded, and never automatically duplicated after ambiguity | passed |
| `M20-DB-01` | One serialized writer plus bounded WAL readers preserves responsive reads and retryable busy semantics | passed |
| `M20-FLT-01` | Runtime/provider/lease/delivery/output faults produce bounded durable or explicit-unknown outcomes | passed |
| `M20-END-01` | Twenty warm restart/fault cycles retain zero child/socket/temp leak and `+3` FD/`+5` goroutine bounds | passed |
| `M20-SEC-01` | Bundle and diagnostics exclude live authority/secrets, preserve private modes, redaction, and path containment | passed |
| `M20-PKG-01` | Two fixed-metadata Linux archives and checksums are byte-identical and self-diagnose after extraction | passed |
| `M20-ALL-01` | Complete normal/race/generated/static/package/prior-scenario/personal-beta gate | passed |

## Test evidence

| Suite | Command | Result |
| --- | --- | --- |
| Complete repository gate | `./scripts/check.sh` | passed |
| Generated query consistency | `./scripts/check-generated-db.sh` | passed |
| Static analysis | `./scripts/go.sh vet ./...` | passed |
| All packages | `./scripts/go.sh test ./...` | passed |
| Full race suite | `./scripts/go.sh test -race -timeout 20m ./...` | passed |
| Personal beta | `./test/scenarios/personal-beta/run.sh` | passed |
| Herdr messaging | `./test/scenarios/herdr-runtime/run.sh` | passed repeatedly |
| Operator terminal | `./test/scenarios/operator-tui/run.sh` | passed |
| Linux candidate | `./scripts/package-linux_test.sh` | passed |
| Formatting/shell/whitespace | repository Go formatting scan; `sh -n` for scenario/scripts; `git diff --check` | passed |

The final complete gate reran every earlier provider-free and recorded-provider
scenario. The final integration pass also proves asynchronous Herdr message wake,
live current-node attach for a blocked held terminal, terminal attach refusal,
and unchanged canonical event truth for inspection.

## Failure and recovery proof

Acceptance injects divergent baseline DDL, unknown events, one corruption in every
semantic family, foreign and missing runtime bindings, all nonquiescent queue
classes, concurrent WAL commits, missing/extra/altered artifacts, unsafe modes,
symlinks, hard links, special files, reserved maintenance paths, source/target
overlap, every publication and activation crash boundary, truncated marker/file
writes, SQLite busy contention, daemon loss, ambiguous wake effects, unavailable
providers, lost runtimes, and repeated restart faults. Recovery either publishes
nothing or a complete independently verifiable result; it never edits canonical
rows as repair.

## Persistence, authority, and security

- The migration ladder is gone. Existing databases open only when application ID,
  sole schema version, embedded source identity, and installed canonical/control
  catalog digest match the current baseline.
- Full verification owns every current authority table exactly once and reports
  the rebuildable FTS projection separately.
- Backup closure contains the exact SQLite snapshot plus referenced immutable
  check/run artifacts. It excludes WAL/SHM, sockets, locks, node identity/key,
  capabilities, provider homes, credentials, live runtime state, and orphans.
- Restore remains inert until explicit activation confirms the source retired.
  Activation re-verifies integrity/quiescence, creates fresh node authority, and
  starts no worker or listener before that proof.
- Hashes detect corruption and inconsistent copying; they do not authenticate a
  bundle against a malicious same-UID actor able to rewrite the manifest.
- Role and purpose strings remain descriptive and never authorize, schedule, or
  prioritize work.

## Known limitations and deferrals

M20 deliberately has no active-run hot backup, point-in-time or incremental
backup, in-place restore, old-schema/bundle import, automatic source retirement,
cloud storage, encryption/signing, multi-node scheduling, provider-session
resume, general filesystem garbage collection, paid-provider load test, or
published installer/package. The Linux archive is an unpublished reproducible
candidate and makes no license claim.

## Decision

- Exit gate satisfied: `yes`
- Waivers: `none`
- Unresolved HIGH findings: `0`
- Unresolved MEDIUM findings: `0`
- Next milestone entry criteria met: `yes`
- Next milestone started: `no`

M20 is accepted at implementation commit
`359522d8aa58a18a7d1151584a8f9bc48b4bfc56`. M21 remains the next milestone and
was not started during this review.
