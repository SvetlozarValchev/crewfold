# `crewfold` command

This directory contains the single Go entry point. M0 implemented `version`,
`help`, and `doctor --self`; M1 added the foreground daemon, status client,
graceful stop, and signal cancellation. M2 added database diagnostics, durable
workspace commands, and event inspection. M3 adds project/checkout commands and
read-only Git observation without creating a second binary.

See [the CLI contract](../../docs/cli.md) and
[technology stack](../../docs/stack.md).
