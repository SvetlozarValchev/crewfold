# Herdr integration

Herdr is Crewfold's preferred interactive runtime driver. Crewfold reuses Herdr's
workspaces, tabs, panes, persistent terminal sessions, named agent processes,
status observation, prompting, waits, and attach experience.

Crewfold does not fork Herdr or store durable coordination state in pane metadata.
It discovers Herdr's installed API schema, uses structured local automation, stores
runtime-handle mappings, and reconciles them after restarts.

Primary upstream references:

- <https://herdr.dev/docs/agent-automation/>
- <https://herdr.dev/docs/cli-reference/>
- <https://herdr.dev/docs/socket-api/>

See [runtime and adapters](../../docs/runtime-and-adapters.md) for the contract and
boundary.
