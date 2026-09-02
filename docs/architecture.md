# Architecture

Crewfold is one Go binary with three current components:

```text
external Codex -- CLI -- owner Unix socket -- room store (SQLite + document files)
      ^                               |
      +------ Codex app-server -------+ room-delivery manager
                                      |
human browser -- loopback HTTP -------+--- hosted-steward manager --- named Herdr/Codex session
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
| `participant.join`, `participant.ack` | Bind a working directory and optional Codex delivery target; advance its read cursor |
| `message.send` | Append a message or publish current context |
| `document.upload`, `document.read` | Share and verify immutable document bytes |
| `steward.start`, `steward.status`, `steward.prompt`, `steward.key` | Start, inspect, and directly steer the room-owned Herdr terminal |
| `steward.stop`, `steward.restart` | Stop it or create a fresh named Herdr/Codex session while preserving room identity |
| `web.bootstrap` | Mint a one-use owner browser URL |

Unix requests and responses are newline-delimited JSON. Browser RPC uses the same
request envelope after the one-time URL is exchanged for an in-memory session.
Unknown input fields are rejected.

## Runtime boundary

External participants retain their native context and working environment.
Codex participants may opt into one narrow runtime adapter: `room join` binds
the current `CODEX_THREAD_ID`, validated through the owner-local Codex app-server
control socket. The delivery manager injects exact new room events with
`turn/start`, which starts an idle turn or steers an eligible active turn. It
does not resume unloaded threads, start external terminals, or capture provider
transcripts. Undelivered events remain queued and the same stable participant
can rebind to a new thread by joining again.

Non-Codex tools and manual scripts join with `--delivery none` and use the CLI
feed directly. Delivery cursors and acknowledgement cursors are separate durable
facts.

The optional hosted steward uses a concrete Herdr CLI adapter. Crewfold creates a
private room workspace, starts Codex with preserved terminal scrollback, and
polls its native status and terminal output. New room events are batched into its
same persistent session only when it is idle. The steward publishes shared output
with an exact Crewfold binary and socket command, so another installed daemon
cannot receive it accidentally. Stop and restart delete the named Herdr session;
the room participant and shared history remain canonical.
