# Buildable repository acceptance scenario

This black-box scenario proves the buildable-repository contract from a clean temporary
directory:

- build the real `crewfold` binary with no network access;
- show root help;
- emit text and JSON version responses;
- embed release version/commit/time through linker metadata without Git access;
- pass text and JSON `doctor --self` checks;
- reject an unknown command with exit code `2`, a useful hint, and no stack trace;
- emit the same unknown-command failure as structured JSON;
- remove its owned temporary directory and binary.

Run it directly:

```sh
./test/scenarios/buildable-repository/run.sh
```

The full repository gate, including formatting, vet, unit tests, race tests when supported,
schema-contract tests, and this scenario, is:

```sh
./scripts/check.sh
```
