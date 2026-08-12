# Contributing to Crewfold

Crewfold is currently design-first. Contributions should preserve the small,
personal control-plane scope while keeping provider and runtime boundaries clean.

## Before implementation

1. Read [docs/vision.md](docs/vision.md) and
   [docs/architecture.md](docs/architecture.md).
2. Check [docs/open-questions.md](docs/open-questions.md) before treating a
   proposed choice as final.
3. Record a consequential, hard-to-reverse decision as an ADR under
   `docs/decisions/`.
4. Prefer one end-to-end capability over several disconnected abstractions.

## Design rules

- Do not put provider-specific concepts in the core domain model.
- Do not make terminal output the source of truth for task completion.
- Do not put secrets or entire transcripts into shared knowledge.
- Do not require network connectivity for normal local operation.
- Do not let an autonomous action exceed the owner's configured policy.
- Do not assume that registered agents are all concurrently running.
- Preserve stable IDs and provenance for every durable object.

## Development workflow

Crewfold is bootstrapped with Go 1.26.5 and vendors its CGO-free SQLite dependency
so the complete gate remains local and offline. Run it before committing:

```sh
./scripts/check.sh
```

The command checks formatting, runs `go vet`, unit/schema tests, race tests when
supported, and every completed capability's black-box scenario. It never invokes a
model provider or uses credentials. See [docs/testing.md](docs/testing.md) for the
long-term test strategy and [docs/stack.md](docs/stack.md) for the proposed later
stack.

Every change should include proportionate tests. Protocol and migration changes
require compatibility fixtures. Runtime adapters require fake or recorded-driver
tests so normal development does not launch paid agent sessions.

## Commits and pull requests

There is no upstream repository yet. Once one exists, changes should be small
enough to review, explain their user-visible effect, and identify any schema,
security, or autonomy-policy impact.

## License

No contribution license can be assumed until the project license and contribution
policy are selected. See [docs/open-questions.md](docs/open-questions.md).
