# Security policy and initial threat model

Crewfold can launch tools that edit code and execute commands. Its local-first
scope reduces the attack surface but does not make it harmless.

## Security status

The repository is pre-release and currently contains design documentation only.
There is no supported production version and no private vulnerability channel yet.
Do not rely on Crewfold to protect sensitive environments until the security model
has executable enforcement and tests.

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
