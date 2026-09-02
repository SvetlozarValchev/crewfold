# Architecture

Crewfold is one Go binary with three current components:

```text
agent terminal -- CLI -- owner Unix socket -- room store (SQLite + document files)
                                      |
human browser -- loopback HTTP -------+
```

The CLI and browser call the same methods. SQLite is authoritative for rooms,
participants, messages, document metadata, and acknowledgement cursors. Document
bytes are stored beneath:

```text
<state>/rooms/<room-id>/documents/<document-id>/<file-name>
```

The database is `<state>/rooms.db`. The schema is initialized directly by the
current binary because this is a pre-release greenfield prototype with one current
format.

## Local method surface

| Method | Effect |
| --- | --- |
| `status` | Read daemon health and room count |
| `room.create`, `room.list`, `room.snapshot`, `room.archive` | Manage rooms |
| `participant.join`, `participant.ack` | Bind a session and advance its read cursor |
| `message.send` | Append a message or publish current context |
| `document.upload`, `document.read` | Share and verify immutable document bytes |
| `web.bootstrap` | Mint a one-use owner browser URL |

Unix requests and responses are newline-delimited JSON. Browser RPC uses the same
request envelope after the one-time URL is exchanged for an in-memory session.
Unknown input fields are rejected.

## Runtime boundary

There is no provider or runtime adapter. A participant may be Codex in Herdr,
Codex in a plain terminal, another AI tool, or a human-operated script. It joins
and communicates through `crewfold room ...` while retaining its native process,
context, and working environment.
