# Owner-granted local checks scenario

This provider-free M17 scenario proves the public CLI, local API, direct check
runtime, and current-packet MCP boundary. Four agents intentionally share the arbitrary
role label `aurora field notebook`; only the agent named by an exact owner grant
can request, list, inspect, or propose repair for allowlisted checks. The checked
task has a durable checkout-anchor run, but checks never mutate its lifecycle.

The scenario records a clean-HEAD failure with bounded redacted logs and honest
subsystem routing, demonstrates that a watcher repair proposal is inert until an
owner accepts it, records a later fresh pass without changing task completion,
changes HEAD and reconciles the pass to monotonic stale evidence, then restarts
the daemon while a separate check child is live and proves exactly-once recovery.
It makes no sandbox, no-network, merge, push, or deployment claim.
