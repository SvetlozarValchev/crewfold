# ADR-0023: Operable workstreams and managed local services

- Status: accepted
- Date: 2026-08-20
- Supersedes: the generalized-process-service deferral in the M23 review and
  the artifact/status-only portions of the M23 owner surface

## Context

ADR-0022 made one existing warmed checkout the execution home for a workstream
and made predecessor handoffs part of successor context. A real Signal Garden
workflow then completed implementation, independent review, remediation, and
verification in that checkout. The canonical dependency chain worked, but the
owner experience exposed four remaining composition failures.

First, a durable agent's read-only owner conversation and its authorized task
runs are stored and rendered as separate surfaces. The reviewer could appear as
`Not Started` while its attached review had already completed. A green
`Completed` badge described process termination even though the review's
assessment was `BLOCK`. This contradicts the accepted single-coworker timeline
and consequential-status contract.

Second, review and verification runs published immutable evidence artifacts,
but the owner and successor had no bounded public operation for reading their
content. An evidence ID is not inspectable evidence, and a
`handoff_with_evidence` edge is not fulfilled byte-for-byte when the successor
can see only that ID.

Third, all accepted tasks became terminal while the Objective remained merely
`active`. Crewfold correctly did not infer owner acceptance from provider prose,
but it also failed to present the exact intermediate state: mechanically
verified delivery awaiting explicit owner acceptance.

Fourth, the verifier started Vite long enough to smoke-test one HTTP response,
then the process disappeared with the finite run. The product definition has
always listed services as domain resources and the domain console reserved a
services surface, but no current persistence, authority, API, supervisor, or
recovery contract exists for a local preview server, watcher, asset cooker,
fixture, or other continuing development process. `crewfold service` manages
the Crewfold daemon itself and is not this missing product capability.

These are one operability problem: an owner cannot yet move cleanly from work
assignment, through evidence-backed delivery, to using and accepting the thing
the crew built.

## Decision

### A managed local service is an attached work resource

A **managed local service** is one owner-reviewed process definition attached to
a domain and optionally one workstream. It is a resource used by work; it is not
an agent, task, hierarchy node, or assertion that delivery is accepted.

The definition freezes:

- one domain and optional workstream;
- one exact checkout plus a contained relative working directory;
- a human name and bounded description;
- an executable and argument array, never an implicit shell string;
- one current execution profile and revision;
- bounded environment additions whose names and values pass the same secret and
  terminal-safety boundaries as launch profiles;
- network exposure policy, loopback-only by default;
- one process, TCP, or HTTP health check (an independent command check would be
  another execution authority and is deliberately not inferred here);
- restart policy (`never`, `on_failure`, or `on_daemon_restart`), bounded retry
  count, and cooldown;
- stop signal and grace period inside fixed owner-safe bounds;
- owning workstream/agent attribution, capacity class, and revision metadata.

The type is generic. Vite is the first real acceptance fixture, not a special
case. The same contract can host a local API, documentation preview, file
watcher, asset cooker, mock dependency, domain-specific test instance, or other
noninteractive long-running development command. One-shot commands remain task
or check runs. Interactive provider work remains a Herdr execution. GUI
applications, containers, external system daemons, remote deployment, and
credential-bearing infrastructure require later explicit adapters rather than
silently inheriting this local process contract.

An ordinary definition cannot use `/bin/sh -c`, `/bin/bash -c`, or another
opaque command interpreter. An advanced owner-only definition may explicitly
authorize one exact interpreter invocation, but no agent grant can manufacture
that authority from prose.

### Definitions and instances are distinct

The canonical definition survives process exit, daemon restart, and backup. A
**service instance** records one node-bound attempt to realize it:

- `requested`, `starting`, `healthy`, `degraded`, `stopping`, `stopped`,
  `failed`, or `unknown`;
- definition and checkout revisions;
- node provenance, generation, process-group binding, and start operation;
- allocated loopback address/port and owner-display URL when applicable;
- bounded health observations, restart attempts, timestamps, exit status, and
  terminal diagnosis;
- immutable bounded/redacted stdout and stderr log segments; and
- request, start, health, restart, stop, and terminal receipts.

PID text is never durable authority. The service supervisor launches one process
group with explicit parent-death and cleanup behavior, verifies node/process
provenance before control, and never attaches to a process merely because a PID
was reused. Child processes are stopped with the group. The first implementation
uses daemon-owned noninteractive supervision rather than Herdr; an advanced
terminal may inspect logs but the service does not depend on an agent's PTY or
conversation lifetime.

A finite task run may request or inspect a service, but terminalizing that run
does not terminalize a separately accepted service instance. Conversely, a
running service does not keep its requesting task successful or unfinished.

### Desired operation is explicit and bounded

Definitions are stopped by default. Start, stop, and restart are distinct typed
operations. No provider response, task completion, package script, or detected
port silently creates a managed service.

The local owner may perform those operations directly. An agent needs a current
service grant freezing domain/workstream, eligible definition or executable
profile, allowed actions, maximum instances, network policy, budget/capacity,
expiry, and delegation envelope. A conversation without that grant may submit an
inert service proposal for exact owner review. A parent may delegate only a
narrower service grant from authority it already holds.

The service launcher resolves the executable through the frozen profile, uses
the exact contained working directory, and supplies only the reviewed
environment. It never installs dependencies, initializes a checkout, opens a
firewall, exposes a remote interface, acquires credentials, or rewrites a package
script as a side effect of start. Missing dependencies produce an attributable
diagnosis and a separately authorized repair path.

### Health, logs, URLs, and ownership are first-class owner data

Every current instance has one bounded owner view containing:

- what command is running and why;
- domain, workstream, checkout, and requesting/owning agent;
- current lifecycle, uptime, restart count, and latest health result;
- its safe local URL when one exists;
- readable structured stdout/stderr with an advanced raw disclosure;
- exact start/stop/restart receipts and terminal diagnosis; and
- the next valid owner action.

HTTP and TCP checks may address only the allocated/local endpoint authorized by
the definition; they are not a general request or SSRF facility. Command checks
are exact argv executions under a separate read-only health profile. Logs are
bounded, valid UTF-8, terminal-safe, redacted, and never become knowledge or
verification merely by being displayed.

The domain view aggregates its services. The workstream view shows its primary
checkout, team, task graph, required handoffs, and services together. The
selected-agent timeline attributes a service proposal or control receipt to the
requesting agent without pretending the service is another agent run.

### One agent timeline tells the complete execution story

The persisted durable conversation epochs, assignments, runs, commands,
changed-path observations, messages, blockers, checks, evidence, handoffs,
service operations, and epoch boundaries remain distinct records. The selected
agent renders them as one ordered timeline.

Status is derived from meaning, not the last process transition. In particular:

- a terminal review run with a blocking assessment renders `Review finished —
  BLOCK`, not generic green `Completed`;
- an idle or never-opened conversation cannot hide active, blocked, failed, or
  recently completed attached task work;
- an attached service remains separately visible after the requesting run
  completes; and
- implementation, review, remediation, verification, and service lifecycle all
  drill into their exact canonical records.

Process/thread identity remains secondary diagnostic detail. The owner should
not need to understand app-server epochs, Herdr panes, or run IDs to know which
coworker did what and what happened next.

### Evidence must be readable by its authorized consumers

Run evidence gains a bounded read operation. The owner may inspect evidence in
its domain/workstream/run scope. A successor whose dependency requires
`handoff_with_evidence` receives capability-scoped read access to the exact
referenced immutable artifacts. It cannot enumerate or read unrelated run
artifacts.

Artifact responses include identity, author/run/task attribution, media type,
byte size, hash, safe inline content for bounded supported types, and an explicit
download path only where the owner-local web boundary can preserve the same
authorization. Unknown, missing, mismatched, oversized, unsafe, or unauthorized
content fails closed. Evidence content is displayed as evidence, never as an
instruction to the browser or agent.

### Verified delivery waits visibly for owner acceptance

Workstream completion is not inferred from provider prose or the last task
becoming terminal. Crewfold derives a non-authoritative delivery summary from
the exact task graph, structured assessments, checks, handoffs, evidence, current
diff/checkout observation, and unresolved blockers.

When all required work is terminal, required evidence exists, no blocking
assessment remains, and final verification passes, the owner surface shows
`verified — awaiting owner acceptance`. The owner can then accept the exact
current delivery revision, reject it with a bounded reason, or reopen/extend work
through a new reviewed graph. Acceptance records one immutable outcome and
closes the workstream; it does not commit, push, merge, publish, deploy, or start
a service unless those are separately authorized exact operations.

### Recovery is fail-closed

Active or starting service instances are actionable external work and make a
quiescent backup cut ineligible. The backup refusal reports bounded service IDs
and counts. After every service is terminal, the bundle contains definitions,
receipts, health history, and referenced immutable log/evidence artifacts, but no
PID, process handle, live socket, node binding, or capability.

Restore creates no service process. Definitions return stopped. Starting one on
the restored node requires ordinary activation plus a new explicit owner or
granted-agent start operation. A source and restored installation can therefore
never control or duplicate the same service merely from backup state.

Daemon restart reconciles only current-node instances. The safe initial rule is
to terminate daemon-owned children through parent-death behavior and create a
new generation only when the frozen restart policy permits it. Unknown process
ownership fails closed and requires owner resolution; Crewfold never adopts a
matching command line or port heuristically.

### Scope remains deliberately local

M24 is not a remote control plane, deployment system, container orchestrator,
credential manager, system-service marketplace, or arbitrary background shell.
It manages a bounded number of owner-reviewed local development processes tied
to canonical domains, workstreams, and checkouts. Remote exposure, Docker/K8s,
GUI/display sessions, databases needing specialized durability, and production
deployment remain explicit future adapter contracts.

## Consequences

- An owner can use the product the crew built without leaving Crewfold or keeping
  an unrelated shell open.
- Vite, local APIs, watchers, cookers, and test fixtures share one generic
  lifecycle rather than accumulating product-specific buttons.
- Conversation, task execution, and service processes remain separate authority
  records while presenting one understandable coworker and workstream story.
- Evidence-backed review becomes inspectable instead of ID-only.
- Final verification becomes an owner-actionable delivery state rather than an
  ambiguously active Objective.
- Backup/recovery gains one more actionable-work quiescence class and never
  clones a live process.
- The current public-release milestone moves to M25; release work does not begin
  until one real browser workflow proves implementation, blocking review,
  remediation, verification, preview service operation, restart, and owner
  acceptance end to end.
