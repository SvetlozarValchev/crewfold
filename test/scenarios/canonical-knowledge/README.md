# Canonical knowledge and provider-switch acceptance

This provider-free black-box scenario exercises only the public CLI against a
real daemon, SQLite database, direct runtime, and the recorded Codex and Claude
executables. Codex first records a durable handoff. The owner then proposes and
accepts an explicitly sourced finding, builds a replacement packet with an exact
revision pin, and binds that packet to a Claude run.

The scenario proves that accepted current knowledge—not terminal output—is the
portable authority. It also covers proposed, stale, task-scoped, over-budget, and
superseded exclusions; exact successor selection; immutable packets across daemon
restart and later supersession; inspectable authority history; durable knowledge
events; and the absence of a unique terminal-log sentinel from both context
packets and the SQLite database.

No credentials, network access, or model inference are used.
