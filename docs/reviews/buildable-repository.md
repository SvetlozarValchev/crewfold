# M0 milestone review — Buildable repository

## Identity

- Milestone: `M0 — Buildable repository`
- Review status: `passed`
- Implementation commit: `7bbe3f58b61e05992471e49d9413d48fabf41bb8`
- Reviewer: Crewfold local implementation gate
- Date: `2026-08-12`

## Demonstrable outcome

- User-visible capability: build one `crewfold` binary and run `help`, `version`,
  and `doctor --self` with text/JSON output.
- Acceptance scenario path:
  `test/scenarios/buildable-repository/run.sh`
- Exact command: `./scripts/check.sh`
- Expected result: all static/unit/race/contract checks pass and the built-binary
  scenario prints `M0 acceptance: PASS`.
- Observed result: passed on Linux/amd64 with Go 1.26.5.

## Test evidence

| Suite | Command | Result | Evidence |
| --- | --- | --- | --- |
| Formatting | `./scripts/check.sh` | passed | No files reported by `gofmt -l` |
| Static analysis | `go vet ./...` via check script | passed | All packages |
| Unit | `go test ./...` | passed | buildinfo, CLI, protocol |
| Race | `go test -race ./...` | passed | All test packages |
| Schema contract | `go test ./protocol` | passed | Three valid unique schemas; version response validated |
| Black-box acceptance | M0 scenario via check script | passed | `M0 acceptance: PASS` |
| Empty module cache | `GOMODCACHE=<empty> GOPROXY=off go test ./...` | passed | No external module dependencies |
| Live conformance | N/A | M0 has no runtime/provider | No network/provider invocation |

## Failure proof

- Injected failure: invoke `definitely-not-a-command` in text and JSON modes.
- Injection seam: public built-binary CLI command selection.
- Expected diagnosis: exit code `2`, `unknown_command`, help hint, empty stdout for
  JSON failure, and no panic/stack trace.
- Observed diagnosis: matched all expected assertions.
- Operation/event IDs: N/A; M0 has no daemon or durable command journal.

A unit-level self-check failure is also injected by making executable discovery
return an error. `doctor --self --output json` returns status `failed` and exit code
`1` with the bounded failure detail.

## Persistence and recovery

- Durable state introduced or changed: none.
- Restart/crash points tested: N/A.
- Reconciliation outcome: N/A.
- Migration fixture added: N/A.
- Backup/restore impact: N/A.

## Security and autonomy

- New actions/capabilities: local process introspection of its embedded metadata
  and operating-system-reported executable path.
- Allowed/denied scope: no project, task, agent, filesystem mutation, credential,
  or network capability exists in M0.
- Secret/redaction impact: version and errors expose only declared build metadata
  and platform; self-doctor does not print the executable path.
- External side effects: none.
- Human approval boundary: N/A.

The normal gate sets `GOPROXY=off`. Release metadata is injected by linker flags and
does not invoke Git or the network at runtime.

## Compatibility

- API/schema changes: introduced CLI version, error, and self-doctor response
  schemas at version `v1` using stable URNs.
- Adapter/runtime compatibility changes: none.
- Earlier milestone scenarios rerun: none exist.
- Upgrade/rollback impact: none; no persistent state exists.
- Module path: deliberately temporary local path `crewfold`; no upstream namespace
  is implied.

## Known limitations and deferrals

- The binary has no daemon, database, project, task, runtime, MCP, Herdr, or
  provider functionality.
- Human help output is text-only.
- There is no packaged installer; development uses Go 1.26.5.
- Daemon/API work is deferred to M1.

## Repository hygiene

- Working tree clean after implementation acceptance: yes.
- No leaked processes/sockets/temp directories: yes.
- No paid/network call in default tests: yes.
- Documentation matches behavior: yes.

## Decision

- Exit gate satisfied: `yes`.
- Waivers: none.
- Next milestone entry criteria met: `yes`.
- Next milestone: `M1 — Daemon and local API spine`.
