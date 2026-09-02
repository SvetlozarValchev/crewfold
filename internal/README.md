# Internal packages

- `appdirs`: native owner-local paths
- `buildinfo`: embedded build identity
- `desktop`: default-browser integration
- `localipc`: Unix-socket and Windows named-pipe transport
- `room`: room store, local/web boundary, and optional hosted-steward Herdr adapter
- `roomcli`: the current public command surface
- `service`: platform background-service integration

The command binary contains no scheduler, repository, task, or general provider
harness. Its only runtime adapter owns the optional room steward's named Herdr
session.
