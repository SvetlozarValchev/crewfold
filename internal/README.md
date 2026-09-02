# Internal packages

- `appdirs`: owner-local XDG paths
- `buildinfo`: embedded build identity
- `room`: room store, Unix/web boundary, and optional hosted-steward Herdr adapter
- `roomcli`: the current public command surface

The command binary contains no scheduler, repository, task, or general provider
harness. Its only runtime adapter owns the optional room steward's named Herdr
session.
