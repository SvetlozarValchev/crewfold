# Architecture

Crewfold has one owner-local Go hub and one restricted collaborator connector.
Neither process owns an AI runtime.

```text
owner browser -- loopback HTTP --+-- room store (SQLite + document files)
                                 |
local agents -- Unix socket -----+-- delivery manager -- codex queue
                                 |                   `-- Claude SendMessage relay
                                 |
remote connector -- tailnet HTTPS/Tailscale Serve
       `-- local delivery relay -- codex queue or Claude SendMessage
```

The CLI and browser call the same room methods. SQLite is authoritative for
rooms, participant identities, messages, document metadata, and cursors.
Document bytes live beneath:

```text
<state>/rooms/<room-id>/documents/<document-id>/<file-name>
```

The database is `<state>/rooms.db`. The stable loopback address and owner-browser
credential are mode-`0600` files in the same owner-private state directory. A
one-use bootstrap is exchanged for the durable browser session and never appears
in its later URL.

## Local method surface

| Method | Effect |
| --- | --- |
| `status` | Read daemon health and room count |
| `room.create`, `room.list`, `room.snapshot`, `room.archive` | Manage rooms |
| `participant.join`, `participant.leave`, `participant.ack` | Bind, deactivate, or acknowledge a participant |
| `message.send` | Append a message or replace participant context |
| `document.upload`, `document.read` | Share and verify immutable document bytes |
| `document.archive`, `document.restore` | Hide or restore one logical document without deleting revisions |
| `web.bootstrap` | Mint a one-use owner browser URL |

Unix requests and responses are newline-delimited JSON. Browser RPC uses the
same request envelope after authentication. Unknown input fields are rejected.

`room.snapshot` returns the newest bounded message window without a cursor.
`after` reads newer events; `before` reads the preceding window. The web console
starts with 50 messages and prepends older windows when the feed reaches the top.
The active participant rail includes only `joined` participants, while departed
identities remain available to attribute historical messages and documents.

## Provider delivery

External sessions retain their native context, terminal, and repository.
Crewfold stores a replaceable provider session ID for each participant and sends
only a compact sequence-range wake-up:

```text
[CREWFOLD · room · #N–#M] Run crewfold room receive room --after N-1 --through M
```

`room receive` returns the exact attributed batch as collapsible command output
and advances the participant's acknowledgement through that cutoff. Ordinary
activity waits briefly so bursts share one turn; direct mentions and
`@everyone` bypass that delay. A bounded maximum delay flushes a continuously
active room.

Codex delivery uses `codex queue` against the open thread captured from
`CODEX_THREAD_ID`. Claude delivery resolves the open session's current name with
`claude agents --json`, launches one disposable tool-restricted Claude process,
and calls `SendMessage` exactly once. The relay then exits. Crewfold never
resumes, starts, or captures the transcript of a participant session. Rejoining
the same handle replaces its delivery target.

## Tailnet boundary

The optional network API is served from the loopback HTTP listener under
`/api/network/*`. Tailscale Serve terminates tailnet HTTPS and forwards to that
listener. It is disabled until the owner records one HTTPS public origin. The
owner RPC remains origin-checked and uses a separate local credential.

Enrollment consumes a single-use room invitation and returns one random device
bearer token; only its SHA-256 digest is stored. Remote RPC permits operations in
granted rooms and never dispatches owner methods. The server derives participant
identity from the authenticated device binding.

The hub stores authorization and pending delivery cursors. Provider targets and
executables stay in mode-`0600` connector configuration on the collaborator
machine. One connector service long-polls all enrolled hubs and performs final
delivery locally through `codex queue` or the Claude `SendMessage` relay. Thus a
remote Codex thread ID or Claude session ID is never sent to the hub.

An enrolled connector can mint a one-use browser bootstrap for a scoped human
console. That session sees only the device's granted rooms and may read,
download, or converse. It cannot create rooms, manage networking, or use owner
document controls.

## Persistence and delivery state

Delivery and read acknowledgement are separate durable facts. A failed provider
injection leaves delivery pending for retry. Leaving marks the participant
`left`, disables delivery, and preserves its messages, context, documents, and
cursors. Rejoining restores the same identity and continues from its preserved
delivery cursor.
