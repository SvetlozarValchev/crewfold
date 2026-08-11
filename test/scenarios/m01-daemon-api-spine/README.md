# M1 acceptance scenario

This black-box scenario proves the M1 daemon and local API spine using the real
built binary and an isolated temporary directory:

- start the foreground daemon and wait through the public `status` command;
- negotiate protocol version 1 and validate structured health output;
- verify the Unix socket is owner-only (`0600`);
- reject a second daemon using the live socket;
- reject a second daemon using the live data directory;
- verify structured request logs contain method and correlation ID;
- kill only the scenario-owned daemon to leave a stale socket;
- restart successfully against that stale socket;
- request graceful stop and prove the owned socket is removed;
- clean up all scenario-owned processes and paths.

Run it directly:

```sh
./test/scenarios/m01-daemon-api-spine/run.sh
```

The in-flight partial-request shutdown case and non-socket-path preservation are
covered by the daemon component tests, where the fault barriers are deterministic.
