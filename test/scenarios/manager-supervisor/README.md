# Manager proposal and deterministic supervisor scenario

This provider-free M16 smoke scenario proves the public delegation boundary and
basic accepted-plan progression while the full adversarial acceptance gate is
still being completed.

The fixture deliberately gives two agents the same arbitrary descriptive role
string. Only the agent whose exact planning task, active assignment, packet-v5
run, and current manager grant all agree can call a proposal tool. The other agent
and a packet-v4 run are denied. Retiring a launch profile or revoking the grant
also denies later use; no role or profile-purpose string changes that result.

The current public smoke path:

1. creates an isolated owner-only home, repository, project, objective, arbitrary
   agent definitions, planning assignment, launch profiles, and supervisor policy;
2. invokes the granted fixture run and submits a typed `A -> B -> independent
   review` proposal through scoped MCP;
3. proves the proposed task titles do not exist before owner acceptance;
4. accepts it once, replays the decision, and proves the exact plan was applied
   atomically with no duplicate effects;
5. manually runs the supervisor under bounded policy, completes A, B, and review,
   and proves one run per task with an inspectable dependency-ready explanation;
   and
6. revokes a grant while its packet-v5 run remains live and proves the later
   proposal call is denied while historical state remains readable.

This smoke path is not the M16 exit gate. The final scenario must still exercise
daemon-background scheduling and restart after durable intent but before worker
launch. Focused tests must prove approval allow/deny; `blocked`, `stale`, `failed`,
wall-time `over_budget`, and `repeated_failure`; global/project/provider/agent/
checkout/claim contention; and run-first stale/lost reconciliation.

Store tests carry the adversarial cases that public commands cannot safely inject:
cycles, cross-scope references, budget edge cases, wrong/stale profiles, claim and
capacity races, run-first lease reconciliation, lost reservations, named
transaction barriers, raw-SQL corruption, concurrent scans, and receipt/hash/link
validation.

The scenario uses only the built Crewfold binary and fixture runtime. It makes no
model, provider-account, credential, remote, or network call. Its shell assertions
use the checked-in JSON helper convention and retain exact diagnostics in the
scenario-owned temporary directory until cleanup reports them.
