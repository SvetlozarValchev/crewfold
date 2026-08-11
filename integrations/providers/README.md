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
