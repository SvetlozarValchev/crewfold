# `crewfold` command

This directory contains the single Go entry point. M0 implemented `version`,
`help`, and `doctor --self`; M1 added the foreground daemon, status client,
graceful stop, and signal cancellation. M2 adds database diagnostics, durable
workspace commands, and event inspection without creating a second binary.

See [the CLI contract](../../docs/cli.md) and
[technology stack](../../docs/stack.md).
