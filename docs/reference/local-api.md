# Local API v1

Status: implemented transport spine in M1. Only `system.hello`, `system.status`, and
`system.stop` exist. Domain commands, subscriptions, and durable events arrive in
later milestones.

## Transport

The daemon listens on an explicitly selected Unix domain socket. M1 accepts one or
more newline-delimited JSON requests per connection and emits one response line per
request.

The socket is created with mode `0600`. The daemon holds an advisory exclusive lock
with mode `0600` on `<data-dir>/daemon.lock` so another daemon cannot use the same
data directory, even with a different socket. A newly created data directory uses
mode `0700`; Crewfold does not silently change the mode of an existing directory.

M1 has no network listener and no remote transport.

## Negotiation

A client begins each connection with `system.hello` and declares its supported
inclusive range:

```json
{"id":"req-1","method":"system.hello","params":{"min_protocol":1,"max_protocol":1}}
```

A compatible daemon selects the highest shared protocol:

```json
{
  "id": "req-1",
  "protocol": 1,
  "result": {
    "type": "hello",
    "selected_protocol": 1,
    "server_min_protocol": 1,
    "server_max_protocol": 1,
    "version": {
      "schema": "urn:crewfold:schema:cli:version-response:v1",
      "version": "dev",
      "commit": "unknown",
      "built_at": "unknown",
      "go_version": "go1.26.5",
      "platform": "linux/amd64"
    }
  }
}
```

If the ranges do not overlap, the daemon returns `protocol_mismatch` with its
supported minimum and maximum. Non-hello requests must carry the selected
`protocol`.

## Envelope

Request:

```json
{"id":"req-2","protocol":1,"method":"system.status"}
```

Success:

```json
{"id":"req-2","protocol":1,"result":{"type":"system_status"}}
```

Failure:

```json
{
  "id": "req-2",
  "protocol": 1,
  "error": {
    "code": "method_not_found",
    "message": "unknown local API method",
    "retryable": false
  }
}
```

The response ID must equal the request ID. Exactly one of `result` or `error` is
present. Request IDs contain 1–128 characters and serve as log correlation IDs;
they are not authorization credentials.

Published JSON Schemas live under `protocol/schemas/local/v1/`.

## Methods

### `system.hello`

Negotiates a protocol and returns the daemon build information. This request does
not carry a `protocol` because selection is its purpose.

### `system.status`

Returns:

- status and selected protocol;
- daemon PID and start time;
- monotonic-derived uptime in milliseconds;
- server build information;
- current request count and whether shutdown is pending.

The status is process health only. M1 has no database or project health to report.

### `system.stop`

Returns `status: stopping`, then closes the listener and all accepted connections.
The server waits for handlers to exit and removes only the socket file it created.

An idle or partially written client cannot hold shutdown open indefinitely.

## Socket startup safety

When the requested socket path already exists, Crewfold behaves conservatively:

| Existing path | Behavior |
| --- | --- |
| Reachable Unix socket | Refuse with `socket_in_use` |
| Socket returning connection refused | Recheck identity/type, then remove as stale |
| Regular file, directory, or symlink | Preserve and refuse with `socket_path_occupied` |
| Socket changes during inspection | Preserve and refuse |

The daemon never recursively deletes a socket parent or data directory.

## Logging

Foreground daemon logs are newline-delimited JSON on stderr. Completed request logs
include `component`, `request_id`, `method`, `status`, `duration_ms`, and an error
code when applicable. Logs do not include arbitrary request bodies.

## Limits and deferrals

- Maximum request line: 64 KiB.
- Local operating-system user is the only identity in M1.
- No subscriptions, commands, event cursors, idempotency, or persistent state yet.
- Unix sockets are the only supported M1 transport; Windows named pipes are later.
- Socket permission is a transport boundary, not future agent authorization.
