# Structured meeting acceptance

`run.sh` builds the real CLI and daemon, then proves three bounded coordination
paths without invoking a model or using the network:

- two independent positions survive a daemon restart, are not recollected, and
  produce an owner-gated sequence proposal that cannot mutate work before
  explicit acceptance;
- a three-participant meeting uses a named reviewer to authorize explicit
  implementer/reviewer role designations; and
- a missing participant stalls durably while preserving the submitted position,
  after which an explicit human takeover applies a typed resolution.

Accepted sequence actions add a real task dependency, release the downstream
overlapping claim, and resolve the coordination hold atomically.
