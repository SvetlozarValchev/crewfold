# Web workbench — M21 design

M21 makes the owner-local browser workbench Crewfold's primary human interface.
It will orchestrate objectives and work, inspect agents, explain delivery, and
render tasks, dependencies, messages, meetings, claims, knowledge revisions, and
audit history.

The implementation has not started. The accepted contract is
[ADR-0020](../docs/decisions/0020-local-web-workbench.md). The workbench will
consume canonical daemon APIs and will not become a second source of truth. CLI
and TUI remain secondary automation, recovery, and terminal-operation surfaces;
Herdr remains an optional interactive runtime host.

## Product mock

[`workbench-mock.html`](workbench-mock.html) is a standalone interactive product
mock for reviewing the proposed local web workbench. It has no backend, makes no
network requests, and is not an implemented Crewfold interface.
