# Durable coordination black-box acceptance

This scenario drives only the built `crewfold` binary and its Unix-socket API.
It proves that provider-neutral agent definitions, objectives, budgets, tasks,
dependencies, optimistic revisions, assignment leases, state transitions,
readiness, status, events, and restart persistence work together without
launching an agent process.

Lease expiry with a controlled clock and simultaneous two-writer contention are
also exercised at the daemon/store component boundary, where time and precise
concurrency can be deterministic.
