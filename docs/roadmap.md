# Roadmap

## Delivery rule

Each milestone must produce one demonstrable end-to-end behavior. Avoid building a
large framework whose components have never coordinated a real task.

## Milestone 0 — Repository and contracts

Status: **in progress**

- Establish product, architecture, domain, security, and protocol documentation.
- Select the license and contribution policy.
- Bootstrap Go module, formatting, linting, unit tests, and local CI.
- Add versioned JSON Schema conventions and compatibility fixtures.
- Create a fake provider and fake runtime driver.

Exit condition: one command can start an empty daemon, initialize a database, and
report health through the local API.

## Milestone 1 — Durable personal control plane

- Workspace, project, repository, checkout, agent, task, and run records.
- SQLite migrations, event journal, projections, and backup command.
- Local Unix-socket API with actor identity and idempotency.
- CLI for CRUD, status, and event watching.
- Deterministic scheduler with one direct fake run.

Demo: create a task, assign a fake agent, launch a fake run, report a handoff, stop
the daemon, restart it, and show the preserved state.

## Milestone 2 — Agent participation

- Run-scoped MCP endpoint and capability credentials.
- Briefing, task, progress, blockage, completion, and inbox tools.
- Durable messages, threads, acknowledgements, and delivery queue.
- Generic terminal provider adapter.
- Direct subprocess runtime with bounded output and reconciliation.

Demo: two different mock/low-cost terminal agents exchange a durable request and
handoff without Crewfold copying one terminal transcript into the other.

## Milestone 3 — Herdr vertical slice

- Herdr capability probe and API-schema compatibility check.
- Workspace/pane creation, named-agent start, prompt, wait, attach, and stop.
- Runtime event observation and restart reconciliation.
- Codex and Claude Code adapter manifests using generic MCP participation first.
- Human status dashboard in the terminal.

Demo: launch an implementer in one Herdr workspace and a reviewer in another,
attach to either, complete a handoff, and resume after a session restart.

## Milestone 4 — Claims and coordination

- Claims and leases for paths/components/operations.
- Git watcher for HEAD, working tree, and touched-path observations.
- Deterministic overlap scoring and policy responses.
- Two- and multi-agent meeting workflow.
- Manager recommendations with human acceptance.
- CI watcher for allowlisted local commands.

Demo: detect two tasks touching one API, gather independent positions, produce an
accepted resolution, sequence the tasks, and wake the dependent agent.

## Milestone 5 — Curated shared knowledge

- Versioned knowledge records and authority rules.
- SQLite FTS5 retrieval with scope, freshness, and provenance ranking.
- Immutable context packets and explainable selection.
- Context deltas for decisions, messages, dependencies, and overlaps.
- Curator proposal queue, contradiction handling, and Markdown export.

Demo: replace a provider session and give the new run enough accepted context to
continue, while proving that unrelated transcripts were not included.

## Milestone 6 — Personal scale and hardening

- Resource and provider concurrency budgets.
- Queue backpressure, retries, cooldowns, and orphan reconciliation.
- Load tests for 100 agent definitions and 100,000 events.
- Security review, redaction tests, backup/restore, database repair guidance.
- Linux release packaging and upgrade/rollback process.
- Opt-in macOS validation.

Demo: operate a simulated fleet of 100 roles with a bounded active set, inject
crashes and stale leases, and recover without losing durable coordination.

## Milestone 7 — Public open-source readiness

- Final license, code of conduct, governance, and maintainer policy.
- Public threat model and vulnerability-reporting channel.
- Adapter SDK and conformance suite.
- Installation, tutorial, examples, and release automation.
- Namespace and trademark review appropriate to a public launch.

No upstream repository should be created until the owner explicitly requests it.

## Post-MVP exploration

- Optional browser console.
- tmux and remote-node runtime drivers.
- External GitHub/GitLab CI and issue adapters.
- Optional local embeddings and retrieval evaluation.
- Policy-tested automatic local integration.
- Multi-machine synchronization.
- Multi-user identity, teams, ownership, and organization control plane.

These are exploration tracks, not promises or prerequisites for a useful personal
Crewfold.
