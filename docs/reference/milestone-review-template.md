# Milestone review template

Copy this file to the future milestone evidence directory and replace every
placeholder. A milestone is not complete while required fields say `pending`.

## Identity

- Milestone: `M? — name`
- Review status: `pending | passed | failed | accepted-with-explicit-waiver`
- Commit: `full commit ID`
- Reviewer: `person or accepted review authority`
- Date: `YYYY-MM-DD`

## Demonstrable outcome

- User-visible capability:
- Acceptance scenario path:
- Exact command:
- Expected result:
- Captured structured result/artifact:

## Test evidence

| Suite | Command | Result | Artifact/log |
| --- | --- | --- | --- |
| Unit | pending | pending | pending |
| Store/migration | pending or N/A | pending | pending |
| Protocol | pending or N/A | pending | pending |
| Component | pending or N/A | pending | pending |
| Black-box acceptance | pending | pending | pending |
| Live conformance | pending or explicit N/A | pending | pending |

## Failure proof

- Injected failure:
- Injection seam/barrier:
- Expected diagnosis and recovery:
- Observed diagnosis and recovery:
- Operation/event IDs:

## Persistence and recovery

- Durable state introduced or changed:
- Restart/crash points tested:
- Reconciliation outcome:
- Migration fixture added:
- Backup/restore impact:

Use `N/A` only when the milestone introduces no durable behavior.

## Security and autonomy

- New actions/capabilities:
- Allowed, denied, wrong-scope, and approval-required tests:
- Secret/redaction impact:
- External side effects:
- Human approval boundary:

## Compatibility

- API/schema changes:
- Adapter/runtime compatibility changes:
- Earlier milestone scenarios rerun:
- Upgrade/rollback impact:

## Known limitations and deferrals

- Known limitation:
- Explicitly deferred behavior:
- Follow-up milestone or issue:

## Repository hygiene

- Working tree clean after acceptance scenario:
- No leaked processes/sockets/temp directories:
- No paid/network call in default tests:
- Documentation matches behavior:

## Decision

- Exit gate satisfied: `yes | no`
- Waivers and accepting authority:
- Next milestone entry criteria met: `yes | no`
- Notes:
