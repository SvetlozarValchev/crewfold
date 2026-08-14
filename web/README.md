# Web workbench — M21 implementation

M21 makes the owner-local browser workbench Crewfold's primary human interface.
It will orchestrate objectives and work, inspect agents, explain delivery, and
render tasks, dependencies, messages, meetings, claims, knowledge revisions, and
audit history.

The first M21 implementation slice is live: a pinned React/TypeScript/Vite shell
is built into content-hashed assets, embedded in the Go binary, and served by the
daemon on exact IPv4 loopback. `crewfold open` obtains a one-time grant through
the owner-only Unix socket, exchanges it for a private browser session, and loads
live daemon status. The shell deliberately renders no invented workspace, project,
task, or agent state while onboarding is unfinished.

The accepted contract is
[ADR-0020](../docs/decisions/0020-local-web-workbench.md). The workbench will
consume canonical daemon APIs and will not become a second source of truth. CLI
and TUI remain secondary automation, recovery, and terminal-operation surfaces;
Herdr remains an optional interactive runtime host.

## Product mock

[`workbench-mock.html`](workbench-mock.html) is a standalone interactive product
mock for reviewing the proposed local web workbench. It has no backend, makes no
network requests, and is not an implemented Crewfold interface.

## Development

```sh
cd web
../scripts/build-web.sh
```

Dependencies and the package manager are exact in `package.json` and
`pnpm-lock.yaml`. `dist/` is committed because Go embeds it and production builds
must not require Node or network access.
