# Web workbench — M21 implementation

M21 makes the owner-local browser workbench Crewfold's primary human interface.
It orchestrates objectives and work, inspects agents, explains delivery, and
render tasks, dependencies, messages, meetings, claims, knowledge revisions, and
audit history.

The M21 workbench is live: a pinned React/TypeScript/Vite application is built
into content-hashed assets, embedded in the Go binary, and served by the daemon
on exact IPv4 loopback. `crewfold open` obtains a one-time grant through the
owner-only Unix socket and exchanges it for a private browser session. From there
the owner can onboard a repository/provider/runtime, talk to one durable
provider-backed project executive, explicitly review and accept closed typed
proposals, inspect crew/Git/logs/evidence/activity, make exact approval decisions,
generate a bounded briefing, run full health, and
open a separately granted current-run terminal. Empty state is always rendered
honestly; the client invents no workspace, project, task, or agent records.

The accepted contract is
[ADR-0020](../docs/decisions/0020-local-web-workbench.md). The workbench will
consume canonical daemon APIs and will not become a second source of truth. CLI
and TUI remain secondary automation, recovery, and terminal-operation surfaces;
Herdr is the normal interactive runtime host and is preflighted before setup or
launch effects. Direct remains an explicit advanced fallback for CI and genuinely
headless environments.

## Product mock

[`workbench-mock.html`](workbench-mock.html) is the standalone design reference
used before implementation. It has no backend and makes no network requests; the
implemented workbench is the React application in `src/` embedded from `dist/`.

## Development

```sh
cd web
../scripts/build-web.sh
```

Dependencies and the package manager are exact in `package.json` and
`pnpm-lock.yaml`. `dist/` is committed because Go embeds it and production builds
must not require Node or network access.
