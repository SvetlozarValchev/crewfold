# `crewfold` command

This directory contains the single Go entry point. M0 implemented `version`,
`help`, and `doctor --self`; M1 adds the foreground daemon, status client, graceful
stop, and signal cancellation without creating a second binary.

See [the CLI contract](../../docs/cli.md) and
[technology stack](../../docs/stack.md).
