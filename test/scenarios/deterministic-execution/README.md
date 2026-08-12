# Deterministic execution acceptance

This scenario proves the first provider-neutral execution loop through the built
CLI, owner-only Unix socket, daemon worker, and SQLite projections.

It verifies:

- explicit placement on an adjacent standalone clone;
- asynchronous progress, completion acceptance, and a durable handoff;
- a blocked run that resumes from its persisted observation cursor;
- runtime start failure without losing the task assignment;
- rejected completion when required evidence is absent;
- daemon restart at an explicit checkpoint followed by durable resume; and
- a task-level timeline spanning the complete run history.

Only the deterministic fake runtime and fake provider are exercised. No external
agent process is launched and no source checkout is mutated.
