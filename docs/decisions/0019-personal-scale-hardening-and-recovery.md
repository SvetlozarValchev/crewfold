# ADR-0019: Personal-scale hardening and quiescent recovery

- Status: accepted
- Date: 2026-08-14

## Context

Crewfold now has one owner-local control plane, durable work and management
projections, external runtime adapters, bounded briefings, and a terminal
dashboard. The personal beta must prove that those facts remain controllable and
recoverable with 100 registered agent definitions, 1,000 tasks, and 100,000
events. It must also give the owner a backup that can be verified without the
source daemon and restored into a different directory without accidentally
controlling the same external runtime twice.

Crewfold's durable directory mixes different kinds of state. `crewfold.db` and
content-addressed evidence are portable facts. A node key, capability token,
opaque runtime binding, Herdr pane, direct-process supervisor record, provider
home, and live output file are operational authority tied to one installation.
Copying all of them would clone authority; copying only the SQLite main file
while WAL or referenced artifacts are changing would not be a coherent cut.

This repository is greenfield. Keeping a migration ladder, accepting an old
database that happens to use the same integer version, exposing deprecated
handle fields, or teaching recovery to translate old bundle formats would add
ambiguity without serving a supported user.

## Decision

### One exact current baseline

Crewfold has one embedded `baseline/current.sql`, one compiled baseline SHA-256,
one fixed SQLite application ID, and one deterministic digest of the installed
canonical/control `sqlite_schema`. Fresh empty storage is created atomically and
then verified. Any nonempty database whose application ID, baseline metadata, or
canonical/control schema digest differs is refused with
`current_baseline_mismatch` before workers or the API listener start. The exact
rebuildable FTS root, shadow tables, and metadata are excluded from that digest
because SQLite can rewrite equivalent virtual-table DDL and the projection may be
missing; their complete expected shape and contents are checked separately and a
failure leaves only explicit index rebuild available. There is no migration
ladder, latest-version comparison, old-schema adoption, import path, or
compatibility branch.

Every application table is registered exactly once as canonical domain state,
durable control/receipt/queue state, or rebuildable derived state. Full integrity
checks cover every registered row, known event type, projection/event/receipt
parity, foreign key, and referenced immutable artifact. A registry-coverage test
fails when a baseline table or durable queue has no auditor rule.

### One path-based maintenance surface

The owner-facing commands are exactly:

```text
crewfold doctor --full --socket <socket>
crewfold backup create --socket <socket> --to <new-bundle-dir> [--idempotency-key <key>]
crewfold backup verify <bundle-dir>
crewfold backup restore <bundle-dir> --to <new-data-dir>
crewfold backup activate <new-data-dir> --confirm-source-retired
crewfold repair inspect <data-dir>
crewfold test load --profile personal-100
```

The bundle path is the only locator. A `backup_<32-lower-hex>` ID is result and
manifest metadata, not a registry name or default-root lookup. Create and restore
targets must not exist; there is no overwrite, merge, in-place restore, or
`--force`. Relative CLI paths are resolved once against the caller's current
directory. The daemon accepts only a canonical absolute target path.

Only two daemon methods are added for recovery:

- `system.doctor.full` with empty parameters; and
- `backup.create` with `target_path` and `idempotency_key`.

Verify, restore, activate, repair inspection, and load generation are local
offline commands. Verify and restore remain usable after the source daemon and
source database are unavailable. Maintenance operations append no coordination
event, so the captured event high-water remains exact.

### A quiescent online cut

Backup creation first uses SQLite's online backup API to produce a standalone
snapshot. All later checks refer to that captured database, not a newer live
view. The captured cut must contain:

- no `requested|starting|active|blocked|stopping|lost` run;
- no unfinished run job, check run, or check job;
- no live run/check runtime binding;
- no pending or leased message wake;
- no `pending|deferred|awaiting_approval|run_requested` scheduling intent;
- no proposed, awaiting-approval, or deferred supervisor action;
- no pending or granted-but-unconsumed approval; and
- no actionable state in any other registered external-effect queue.

An enabled supervisor policy, active grant without a live source capability,
pending inert owner proposal, or task assignment is durable configuration rather
than an in-flight external effect and does not block the cut. It becomes active
again only after restore activation. A source may continue after the snapshot;
post-cut changes are not part of the bundle.

The bundle contains exactly:

```text
manifest.json
manifest.sha256
crewfold.db
check-artifacts/<first-two-hex>/<sha256>
run-artifacts/<first-two-hex>/<sha256>
```

Only artifacts referenced by the snapshot are copied. The bundle excludes
WAL/SHM files, daemon locks and sockets, maintenance receipts and staging,
`node.key`, capability tokens, active runtime/check-runtime state, Herdr sessions
and transcripts, provider homes and credentials, source repositories/checkouts,
daemon logs, and orphan files.

The canonical manifest records its schema, backup ID, creation time, exact
baseline/installed-schema/logical-state hashes, event high-water, quiescence
counts, one `database` object with path/size/SHA-256, and a sorted artifact
path/kind/integer-mode/size/SHA-256 entry list. `entry_count` and `total_bytes`
count the database plus artifacts; the two manifest envelope files are not
entries. Directory mode is integer `448` (`0700`) and file mode integer `384`
(`0600`). A verifier rejects missing or extra entries, path
traversal, symlinks, aliased hard links, non-regular files, unsafe modes,
truncation, content mismatch, unknown schema, baseline mismatch, logical-state
mismatch, and canonical-integrity failure.

Bundle hashes detect damage and inconsistent copying. They do **not** establish
authenticity against a malicious process running as the owning UID that can
rewrite both manifest and contents. The exact database also contains sensitive
messages, evidence, and registered checkout paths; a private bundle is not a
redacted export.

### Runtime identity stays on one node

Opaque runtime and provider handles are internal node-bound bindings. They are
removed from public run/check records and event payloads, exist only while a run
or check is live, and are cleared on terminal transition. Attach, prompt,
interrupt, stop, wake, and reconciliation require a live state and a binding to
the current node key. A terminal Herdr pane is not controllable through
Crewfold.

Before a normal terminal run clears its binding, Crewfold archives redacted,
bounded stdout and stderr as immutable content-addressed run artifacts. Each
stream retains at most 64 KiB plus captured/omitted byte counts, truncation, and
content hash. `run.logs` reads a live binding while live and immutable artifacts
while terminal. An untrusted/lost runtime that cannot yield trustworthy logs is
recorded explicitly and returns `run_logs_unavailable`, never an empty successful
capture. Full Herdr transcripts and active runtime directories are not archived.

A `lost` run keeps its capacity and blocks backup because it may still write. The
owner must retire the runtime through its native control surface and then use:

```text
crewfold run resolve-lost <run-id> --workspace <scope> \
  --expected-revision <revision> --note <text> \
  --confirm-runtime-retired --socket <socket> [--idempotency-key <key>]
```

This owner-only mutation never attempts an external stop. It records
`run.lost_resolved`, changes the run to failed with
`runtime_retired_by_owner`, clears its node binding, releases capacity, and
leaves the task blocked for an explicit retry/reassignment decision.

### Restore is inert until disaster-recovery activation

Restore verifies the bundle, constructs a private sibling staging directory,
copies the standalone database and referenced artifacts, writes a sealed
`.restore-pending.json`, and atomically renames into a nonexistent target. It
creates no node key, capability, runtime state, or event.

A daemon refuses a pending target before database recovery, capability
initialization, workers, runtime drivers, socket creation, or listener startup.
`backup activate --confirm-source-retired` takes the target lock, repeats the
exact baseline/full-integrity/artifact/quiescence checks, records the owner's
source-retirement assertion, generates a fresh node key, creates empty
capability/runtime roots, and seals the activated state without changing domain
rows or the event cursor.

The first daemon startup verifies the activation digest and quiescence before any
mutation or external call. Injected nonterminal work or a binding fails with
`restore_unsafe_nonterminal`. Activation intentionally does not require a
reachable source: the confirmation is the owner's disaster-recovery assertion.
The new key and handle-free cut prevent the restored node from controlling the
source's external runtimes; source retirement prevents parallel fresh launches
against the same checkouts.

### Personal-scale admission and proof

Workspace supervisor concurrency limits apply to manual and supervised starts;
`enabled` controls automatic policy evaluation only. Defaults are eight
unresolved runs, two requested/starting runs, four per project, and four per
provider. A node-wide nonconfigurable ceiling allows at most 20 unresolved runs.
`requested|starting|active|blocked|stopping|lost` consume unresolved capacity;
`requested|starting` consume starting capacity. Admission and run creation are
one transaction. A refusal returns retryable `execution_capacity_exhausted` and
appends no event.

Message send commits durable mail and wakes a dedicated worker; it never calls an
external runtime inline. Wake is best-effort and at-most-once: restart converts
an uncertain executing wake to visible `failed_unknown` rather than issuing a
duplicate prompt. Supervisor/check-watch/reconciliation passes use bounded
keyset pages, and no database transaction remains open across an external call.

The deterministic `personal-100` profile contains one workspace, ten projects,
100 arbitrary-role agent definitions, 1,000 tasks, exactly 100,000 known events,
and 80,000 events in one noisy project. It makes no model, network, provider-home,
or user-data call. It proves briefing fairness and the fixed 128-claim/64-KiB
bounds.

The absolute Linux gate is: warm startup at most two seconds; saturated status
and message p99 at most one second and maximum two seconds over 200 operations;
project briefing p99 at most two seconds/maximum five; workspace briefing p99 at
most five seconds/maximum ten over 20 reads; doctor/create/verify/restore at most
60 seconds each; load generation and verification at most five minutes; peak RSS
at most 512 MiB; and database plus referenced fixture artifacts at most 1 GiB.
Relative benchmark deltas are diagnostic, not the only gate.

## Consequences

- A backup is a portable, exact, private recovery cut without cloned operational
  authority.
- Recovery cannot preserve or take over a live provider/terminal session. Work
  must reach a trusted terminal state first.
- Terminal logs remain inspectable because bounded redacted evidence is separated
  from process identity.
- Old local databases and bundles stop at a stable current-baseline error instead
  of receiving an untested transformation.
- Offline verification and repair guidance remain useful when the daemon cannot
  start.
- The personal beta has absolute resource and responsiveness claims rather than
  hardware-relative benchmark prose.
- Role and purpose strings remain descriptive and never become authority or
  scheduling inputs.

## Rejected alternatives

- Copy database, node key, capabilities, and runtime directories together: this
  clones live authority and permits two installations to control one runtime.
- Sanitize handles only while building a bundle: that makes the backup differ
  from its asserted database/event cut and leaves public/event leakage in the
  current product.
- Copy only `crewfold.db`: a WAL-mode main-file copy and missing referenced
  artifacts are not a coherent snapshot.
- Permit active or lost runs in a backup and rewrite them on first startup:
  restore would invent domain events/outcomes and could race an external process.
- Preserve full Herdr sessions or provider-native transcripts: those are runtime
  implementation state, not portable Crewfold coordination.
- Use backup IDs with an implicit registry/root: offline verification and restore
  would depend on unavailable source state and ambiguous path resolution.
- Restore in place or offer `--force`: recovery must not destroy the only source
  copy or merge two authority graphs.
- Automatically repair, migrate, salvage, or edit canonical rows: M20 inspection
  gives exact guidance; only explicit derived-index rebuild and restore-to-new-
  directory are supported.
- Treat hashes as signatures: a same-UID attacker can rewrite the payload and
  hashes together.
- Infer a larger launch allowance from an agent's role or purpose: arbitrary
  owner labels confer no authority.

M20 explicitly does not add active-run hot backup, point-in-time/incremental
recovery, old-schema/old-bundle conversion, cloud backup, encryption/signing,
multi-node failover, automatic source retirement, general artifact/event GC,
provider-native session resumption, paid-provider load, a replacement UI, public
package publication, or a license decision.
