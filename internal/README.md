# Internal packages

- `appdirs`: owner-local XDG paths
- `buildinfo`: embedded build identity
- `room`: room store, Unix/web boundary, and provider delivery management
- `codexqueue`: compact delivery into open Codex threads
- `claudequeue`: compact delivery into open Claude Code sessions
- `roomcli`: the current public command surface

The binaries contain no scheduler, repository, task, or provider-runtime
harness. Delivery adapters address sessions users already run elsewhere.
