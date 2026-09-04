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
format. The stable loopback address and owner-browser credential live in
mode-`0600` files in the same owner-private state directory. The one-use
bootstrap never appears in the durable browser URL.

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

`room.snapshot` returns the newest bounded message window when no cursor is
provided. `after` reads newer events for live updates; `before` reads the
preceding bounded window for upward history pagination. The web console starts
with 50 messages and prepends older windows as the feed reaches the top.

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
polls its native status and terminal output. Onboarding establishes the quiet
facilitator and curator policy once. Successful onboarding establishes the
current room sequence as the delivery baseline, so existing history is not
immediately injected a second time. New room events are then quiet-period batched
into bounded deltas and delivered to the same persistent session only when it is
idle. A direct mention bypasses the batching delay. The delta contains the room,
sequence range, explicit-address flag, and exact included events; it does not
repeat onboarding, current context, or document contents. The steward retrieves
those durable records on demand through the CLI when a delta may supersede them.

Conversation and curation have separate thresholds. Direct participant exchanges
default to no chat action, while explicit mentions, synthesis requests,
unresolved contradictions, unresolvable blockers, and consequential owner
decisions may trigger one public response. Material corrections, invalidated
conclusions, resolved contradictions, and completed phases can instead update the
replaceable context or revise a same-named shared document without interrupting
the feed. Crewfold advances the hosted steward's delivery and acknowledgement
cursors together only after Herdr reports that the injected Codex turn settled;
a failed or interrupted delivery remains pending for retry.

The steward publishes with an exact Crewfold binary and socket command, so
another installed daemon cannot receive it accidentally. An explicit steward
stop or restart deletes the named Herdr session; the room participant and shared
history remain canonical. Restarting only the Crewfold daemon recreates its
disposable Herdr host and resumes the initialized steward's last Codex thread in
the room-owned working directory. It must not fork an empty conversation, enqueue
onboarding, or publish another introduction.
