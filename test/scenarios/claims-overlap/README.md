# Claims, overlap, and drift acceptance

`run.sh` builds the real CLI, starts the real daemon, registers adjacent clones
and a shared checkout, then proves the complete coordination path:

- two tasks declare overlapping scopes from different checkout directories;
- overlap severity, policy response, concrete witness, and explanation are
  deterministic and queryable;
- `deny_new` rejects atomically;
- a shared checkout emits the explicit no-filesystem-isolation warning;
- a file changed while the watcher is stopped is found after restart with an
  observation-gap marker; and
- the drift record does not rewrite the task's declared claim.

The scenario is local-only. It makes no model call and performs no network
access.
