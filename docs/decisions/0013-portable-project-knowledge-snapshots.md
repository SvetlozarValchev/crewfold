# ADR-0013: Portable project knowledge snapshots

- Status: accepted
- Date: 2026-08-13

## Context

Canonical knowledge is valuable only if the owner can recover it without a
provider session, terminal transcript, FTS index, or the original Crewfold data
directory. Copying the SQLite file is a different operational problem: a live
database also has a WAL, node-local event sequences, capabilities, runtime state,
and many coordination records that do not belong in a portable knowledge
artifact.

The first portable format must be small enough to implement and test as its own
vertical slice. In particular, replaying origin events would falsely recreate
authority belonging to runs, meetings, tasks, and curator policy that are not in
the bundle. Importing into an arbitrary nonempty project would also need identity
mapping and conflict policy that Crewfold does not yet have.

## Decision

### One complete project snapshot

`knowledge export` produces one complete project-scoped snapshot. It includes all
canonical knowledge items and their complete revision histories, ordered source
references, effective project/task applicability, and all portable contradiction
records whose participants are in the project. Proposed, rejected, current,
stale, and superseded revisions and proposed, open, dismissed, and resolved
contradictions are all data; export does not select only currently retrievable
records.

The format deliberately excludes the origin event journal, authority-check
ledgers, curator rule/derivation/auto-acceptance rows, and idempotency state. Actor,
decision, lifecycle, timestamp, and note fields already present in the canonical
revision or contradiction snapshot remain descriptive fields, but the bundle is
not proof of the origin node's governance history. An import is a new local-owner
trust decision over the complete validated snapshot.

Source tuples are preserved as descriptive origin references. An imported task
source ID never acquires exact-task or dependency-affinity ranking merely because
an unrelated local operational task has the same opaque ID. Applicability remains
the explicit project/task scope attested by the importing owner.

The snapshot also excludes projects other than the selected project, operational
tasks, objectives, meetings, agents, runs, messages, claims, context packets,
artifacts, provider state, transcripts, capabilities, credentials, checkouts,
repositories, FTS rows, embeddings, and retrieval metadata. It is neither a full
database backup nor a coordination-state migration.

### Two deterministic files

The bundle is a directory containing exactly `manifest.json` and `knowledge.md`.
The manifest is the machine authority. Markdown is a deterministic human-readable
rendering and cannot override the manifest.

The manifest `schema` is
`urn:crewfold:schema:portable:knowledge-bundle-manifest:v1` and its `type` is
`portable_knowledge_bundle`. Import receipts use
`urn:crewfold:schema:portable:knowledge-import-receipt:v1`; local export/import
results use `urn:crewfold:schema:local-api:knowledge-export-result:v1` and
`urn:crewfold:schema:local-api:knowledge-import-result:v1`, respectively.

Crewfold canonical JSON v1 uses fixed struct field order, explicitly sorted
arrays, compact encoding, UTF-8, HTML escaping disabled, and one trailing LF. It
accepts no unknown fields. The manifest contains versioned schema identity, exact
workspace/project identity, portable task-applicability anchors, items,
revisions, sources, contradictions, record counts, and the SHA-256 digest of the
Markdown file. It contains no generated-at time or node-local event cursor, so
unchanged canonical project state produces byte-identical files on another
export. The command result reports the coherent source event cursor separately.

`content_sha256` is the SHA-256 digest of the compact canonical snapshot encoding
before its trailing LF. `bundle_id` is `kbun_` plus the first 32 hexadecimal
characters of that digest. The manifest separately freezes Markdown byte size and
SHA-256. Import accepts the full expected content digest and checks both digests
before any database mutation. Checksums provide integrity and deterministic
identity, not authenticity or a cross-organization signature.

### Exact applicability without ghost work

Portable task applicability is not an operational task. Crewfold persists an
immutable task-scope anchor containing the exact task ID and its workspace/project
binding plus original creation time/actor identity. A native task-scoped proposal
creates or reuses that anchor; migration backfills anchors for existing scoped
items. Imported item bindings refer to the anchor rather than requiring a row in
the operational task table.

This preserves fail-closed behavior: project-only retrieval still cannot expose a
task-scoped revision, while import never fabricates a task, assignment, agent,
run, meeting, or capability. An anchor becomes usable by an exact task operation
only when that operational task really exists with the same project binding.
An existing or later task with the same opaque ID must also match the anchor's
workspace, project, creation time, and creator; a collision cannot activate the
portable applicability under a different task identity.

### Owner-only exact import

Import is available only on the owner-local API and CLI. It is not an MCP tool or
run capability. The target workspace and project are required and must exactly
match the manifest. Without `--create-scope` they must already exist. With the
flag, Crewfold may create the exact missing workspace, project, and portable task
anchors, but still creates no repository, checkout, or operational source entity.

The target project's canonical knowledge scope must be empty. V1 does not remap
IDs, merge partial histories, overwrite records, choose a newer revision, or
resolve a collision. Repeating the same exact bundle is idempotent under the same
or a new request key and appends no second import event. Reusing an identity with
different bytes or importing another bundle into a nonempty canonical scope fails
before mutation.

Import treats files as untrusted input. It verifies path type, file listing,
byte/count bounds, schemas, checksums, IDs, content hashes, ordering, item/revision
lifecycle combinations, source/applicability bindings, supersession structure,
contradiction pairs, and contradiction lifecycle structure before writing. The
validated snapshot, local owner import attestation, import receipt, canonical
rows, task anchors, and completion event commit in one transaction. The immutable
receipt recognizes later exact retries. A failure or process interruption leaves
none of them visible.

Stable public errors are `knowledge_export_path_exists`,
`invalid_knowledge_bundle_path`, `invalid_knowledge_bundle`,
`knowledge_bundle_digest_mismatch`, `knowledge_import_scope_conflict`, and
`knowledge_import_conflict`. The ordinary mutation contract also applies:
reusing one idempotency key with a different normalized import request returns
`idempotency_conflict`, and an unexpected durable I/O/database failure returns
`storage_failed`. The store retains `knowledge_import_denied` as an
unreachable-through-v1 defense if a non-owner actor reaches it.

V1 caps each file at 64 MiB, each snapshot at 4,096 items, 16,384 revisions,
and 8,192 contradictions. Exceeding a byte or record bound is an invalid bundle,
not an invitation to truncate.

The import attestation is the local authority for imported accepted/current state
and for any imported open contradiction's quarantine effect. Origin authority
checks are not forged or replayed. Later local governance uses the ordinary
authority/event paths and can be exported as the resulting newer canonical
snapshot.

### Filesystem boundary

Export creates a new private directory and never overwrites an existing path.
Directory mode is `0700`; both files are regular, non-symlink files with mode
`0600`. It writes a private sibling temporary directory, syncs complete files,
and renames atomically. Safe publication requires each reachable ancestor to be
owned by root or the daemon user; a group/world-writable ancestor reachable
before an owner-private boundary must be sticky, so ordinary `/tmp` paths remain
supported. Import accepts only a real directory with exactly the two expected
regular files, rejects symlinks and path traversal, and enforces bounds while
reading. The owner should treat the bundle as sensitive because it contains
complete canonical bodies and governance notes.

If the final parent-directory sync fails after the atomic no-replace rename,
export returns `storage_failed` even though the complete bundle may already be
visible at the requested path. Crewfold does not delete or overwrite that
commit-uncertain result; the owner must inspect the existing directory and either
retain it or remove it before exporting again.

## Consequences

- Canonical decisions, findings, applicability, revision history, and active
  contradiction effects can move without provider-private state or a healthy
  retrieval projection.
- The artifact is byte-stable, inspectable, provider-free, and independently
  testable through export/import/re-export.
- Import cannot silently broaden task-scoped knowledge or create runnable work.
- The owner explicitly assumes trust in imported final state. The bundle does not
  preserve the origin authority ledger or establish who was genuinely authorized
  on another node.
- Exact-scope import is suitable for portable recovery and controlled copying, not
  collaboration between divergent nonempty projects.
- Signed bundles, authority/evidence portability, project/ID mapping, partial or
  incremental export, merge policy, archive compression, redaction, remote
  registries, and full database backup/restore remain future work.

## Rejected alternatives

- Replay origin events locally: node-local sequence, absent principals, and absent
  structured sources would turn descriptive history into forged local authority.
- Restore tasks or meetings as placeholders: that would pollute scheduling and
  source-authority projections with entities that never existed locally.
- Import into an arbitrary nonempty project: safe collision and divergence policy
  is a separate merge feature, not an import retry rule.
- Export only accepted/current records: it loses rejected, stale, superseded, and
  contradiction history needed to explain the current snapshot.
- Treat Markdown as input authority: presentation syntax is not a safe canonical
  interchange format.
