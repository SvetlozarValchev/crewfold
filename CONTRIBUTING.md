# Contributing

Crewfold is intentionally small. A change should make independently running
agents easier to connect to one room without adding task engines, general
provider harnesses, compatibility paths, or runtime ownership beyond the single
optional hosted room steward.

Before changing behavior, read [docs/product.md](docs/product.md) and
[docs/architecture.md](docs/architecture.md). Add proportionate tests and run:

```sh
./scripts/check.sh
```

The normal gate is local and does not invoke an AI provider. Source edits use the
current room schema only; this pre-release prototype does not carry migration or
deprecated API layers.
