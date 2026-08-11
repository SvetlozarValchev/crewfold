# M2 acceptance scenario

This black-box scenario proves the M2 persistent workspace and event journal with
the real built binary and an isolated data directory:

- start a daemon and inspect SQLite health through `doctor --database`;
- verify the database file is owner-only and uses schema version 1 in WAL mode;
- initialize a workspace and capture its stable ID, revision, and event cursor;
- replay the same idempotency key and receive the byte-identical result;
- reject a duplicate name under another key without appending an event;
- inspect the immutable `workspace.created` event after cursor zero;
- stop and restart the daemon with the same data directory;
- retrieve the byte-identical workspace record by stable ID after restart;
- stop cleanly and leave no daemon or socket behind.

Run it directly:

```sh
./test/scenarios/m02-persistent-workspace/run.sh
```

Process-death atomicity is covered by the daemon component test. It pauses a real
helper daemon after the projection write and after the event append, kills the
process at each barrier, restarts against the same database, and proves the
workspace, event, and idempotency record were all rolled back.
