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

## Implemented fixture boundary

Crewfold probes the installed `herdr api schema --json` document and the selected
live session before launch. The current compatibility contract is API schema 1,
protocol 19 (Herdr 0.8.0), with the workspace, snapshot, pane input, process,
read, and close methods used by the driver. An incompatible schema blocks launch
with an actionable doctor result.

Each run receives an isolated Herdr workspace. Crewfold stores workspace, tab,
pane, and terminal identifiers, but treats the terminal identifier as the stable
identity: moving the pane can change all layout-qualified IDs without changing
the Crewfold run. A provider-neutral pane supervisor keeps the child attached to
Herdr's PTY and records only operating-system lifecycle. Completion still comes
from the provider/MCP report and acceptance policy.

Useful commands:

```sh
crewfold doctor --runtime herdr
crewfold run start TASK_ID --runtime herdr --provider fixture-terminal ...
crewfold run prompt RUN_ID --text "check your Crewfold inbox" ...
crewfold run interrupt RUN_ID ...
crewfold run attach RUN_ID ...
```

The same runtime can host the implemented headless Codex provider; provider
completion remains MCP-authoritative and terminal output remains diagnostic. The
regular offline gate uses a stateful recorded Herdr CLI endpoint. Installed Herdr
conformance is explicitly opt-in and creates a dedicated session:

```sh
CREWFOLD_LIVE_HERDR=1 ./test/live/herdr/run.sh
```

The combined real Codex/Herdr canary has a separate model-usage gate documented in
[`test/live/codex/run.sh`](../../test/live/codex/run.sh).
