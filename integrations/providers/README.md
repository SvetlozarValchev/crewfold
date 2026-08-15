# Provider integrations

This directory will contain first-party provider adapters and their conformance
fixtures. Initial targets are:

- a fake deterministic adapter for tests;
- a generic terminal adapter;
- Codex enhancements;
- Claude Code enhancements.

Provider adapters normalize capabilities, launch preparation, lifecycle
observations, native resume handles, usage metadata, and MCP configuration. They do
not own Crewfold tasks, messages, permissions, or knowledge.

Provider-specific code should remain at this edge. The core schedules capabilities,
not brand names.

## Implemented Codex canary edge

The `codex` adapter currently uses the stable headless CLI rather than embedding a
provider SDK or depending on the experimental app-server protocol. It:

- probes `codex --version`, `codex exec --help`, a no-effect
  `codex sandbox linux` invocation, and `codex login status` without making a
  model call;
- launches `codex exec --json --ephemeral --ignore-user-config` in the checkout
  selected by Crewfold;
- injects one required STDIO MCP server through inline config without modifying
  `~/.codex/config.toml` or project config;
- forwards the Crewfold socket and capability-file path by name while a small
  bridge reads and injects the private token locally;
- treats MCP reports as progress/blockage/completion authority and the process
  JSONL stream as diagnostic lifecycle evidence only; and
- recognizes explicit auth, MCP-startup, approval, and permission boundaries
  without interpreting a normal process exit as completion.

The adapter does not yet own an interactive Codex thread, native resume, active
turn steering, or usage accounting. A later contract can use Codex app-server for
those capabilities without changing Crewfold tasks, reports, messages, or runtime
drivers.

Current upstream contracts:

- <https://learn.chatgpt.com/docs/developer-commands?surface=cli>
- <https://learn.chatgpt.com/docs/extend/mcp?surface=cli>
- <https://learn.chatgpt.com/docs/app-server>
