# Security policy and initial threat model

Crewfold can launch tools that edit code and execute commands. Its local-first
scope reduces the attack surface but does not make it harmless.

## Security status

The repository is pre-release. The local API, durable coordination core, and a
fixed provider-free direct-process fixture have executable enforcement and tests,
but arbitrary agent commands, provider credentials, network policy, and an
operating-system sandbox are not implemented. There is no supported production
version or private vulnerability channel yet. Do not rely on Crewfold to protect
sensitive environments.

## Trust boundaries

Crewfold treats the following as separate trust domains:

- the human owner;
- the local Crewfold daemon and database;
- each repository and its potentially untrusted contents;
- each model provider and agent process;
- runtime drivers such as Herdr;
- external systems such as GitHub, CI, issue trackers, and hosted model APIs;
- plugins and MCP clients.

Repository text, issue content, terminal output, and agent messages are data, not
instructions to Crewfold's privileged control plane.

## Required controls

- Bind the default API to a user-only local socket.
- Restrict socket and database permissions to the owning user.
- Store references to secrets, not plaintext secrets, in durable coordination
  records.
- Redact credentials from captured output and knowledge proposals.
- Separate observation, recommendation, and execution permissions.
- Require explicit policy for network access, pushes, merges, destructive Git
  operations, deployments, and messages to real people.
- Give each run a workspace boundary, environment allowlist, and resource budget.
- Record privileged actions and their initiator in an append-only audit stream.
- Treat generated context packets as untrusted input to the receiving model.
- Validate every adapter event and reject unsupported protocol versions.

## Implemented direct-process boundary

The current `direct` driver only launches Crewfold's fixed fixture provider. Its
working directory is selected from a registered checkout, its inherited
environment is allowlisted, stdout and stderr are independently capped, and API
log responses heuristically redact secret-like assignments. Owner-only raw capture
files under the daemon data directory can still contain provider-emitted secrets;
they are diagnostic evidence, not shared context, and must be treated as sensitive.
The driver supervises process groups and distinguishes exit, timeout, requested
stop, and unknown process identity across daemon restarts.

This boundary is process supervision, not containment. Running arbitrary project
or model-provider commands remains disabled until command policy and an explicit
sandbox decision are implemented.

## Initial autonomy classes

| Class | Examples | Default |
| --- | --- | --- |
| Observe | Read status, diffs, tests, messages | Allowed inside registered projects |
| Coordinate | Claim work, send agent mail, request review | Allowed and audited |
| Mutate local | Edit files, run tests, create a worktree | Allowed per task policy |
| Mutate shared | Push, merge, change remote issue state | Human approval required |
| High impact | Deploy, delete data, rotate credentials | Out of scope initially |

## Reporting vulnerabilities

A reporting address will be added before the first public release. Until then,
report issues directly to the repository owner through an agreed private channel;
do not place secrets or exploit details in public issue trackers.
