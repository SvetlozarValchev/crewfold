# Portable project knowledge acceptance

This provider-free black-box scenario exercises deterministic project knowledge
export and exact owner-only import through the public CLI and two independent real
daemon/SQLite data directories.

The source corpus contains project-wide and task-scoped knowledge, multiple
ordered task sources, proposed/rejected/current/stale/superseded revisions, and
proposed/open/dismissed/resolved contradictions. Repeated exports must contain
exactly `manifest.json` and `knowledge.md`, use private modes, and remain
byte-identical across distinct destinations, restart, and a missing FTS projection.

The fresh target imports the exact bundle with `--create-scope`. The scenario
proves exact IDs, bodies, status, sources, task applicability, and contradiction
effect survive import and restart; no source task, meeting, agent, run,
repository, or checkout is fabricated. Immediate re-export is byte-identical.
Imported detail has no forged origin authority-check rows; only the local import
attestations appear in the target journal. Same- and new-key replays append no
event. After explicit retrieval rebuild,
project-only search does not leak task-scoped knowledge, an imported open pair is
quarantined, and a new explicit context build with the broad participant fails
atomically.

Existing destinations, symlinks, missing/extra files, tampered content, wrong
expected digest/scope, and a different valid bundle over a nonempty target all
fail without partial target state; before/after durable count fingerprints cover
canonical rows, anchors, receipts, import attestations, events, and idempotency.
The scenario uses no `jq`, provider
executable, model, credential, network, or transcript.
