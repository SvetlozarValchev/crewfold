# `crewfold` command

This directory contains the single Go entry point for self-diagnostics, the
foreground daemon, database/workspace/event operations, read-only project and
checkout observation, and durable agent/objective/task coordination. It does not
contain a runtime launcher or create a second provider-specific binary.

See [the CLI contract](../../docs/cli.md) and
[technology stack](../../docs/stack.md).
