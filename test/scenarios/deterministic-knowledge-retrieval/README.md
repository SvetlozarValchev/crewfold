# Deterministic knowledge retrieval acceptance

This provider-free black-box scenario exercises deterministic canonical-knowledge
search through the public CLI and a real daemon/SQLite database. Its fixture corpus
contains task-local and project-wide records, direct-task and dependency
provenance, confidence and verification differences, title/body text matches, and
textually strong candidates that are proposed, stale, expired, scoped to another
task, or stored in another project.

The scenario proves:

- exact-task applicability and task/dependency provenance precede later quality
  and text-score ranking axes;
- broad project search never exposes task-local knowledge;
- proposed, stale, expired, wrong-task, and wrong-project revisions never become
  candidates merely because their text matches;
- search is read-only and leaves canonical knowledge, context packets, and the
  event journal byte-for-byte unchanged;
- making the FTS contents inconsistent and removing the derived table each make
  search explicitly `retrieval_degraded` while exact canonical reads remain
  available;
- retrieval status and `doctor --retrieval` diagnose the failure;
- an idempotent public rebuild reconstructs the index without changing canonical
  state or advancing its generation on replay; and
- the complete repaired revision order and index identity survive daemon restart.

The scenario compiles a tiny disposable Go helper into its temporary directory to
delete one derived FTS row and later drop the derived FTS virtual table while the
daemon is stopped. That helper is failure injection, not a product interface. All
asserted product behavior uses public Crewfold commands. No provider executable,
credentials, model inference, network access, or transcript is involved.
