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
| `task.list`, `task.show`, `task.create`, `task.update`, `task.archive`, `task.restore` | Optional shared work records; metadata changes notify subscribers when present |
| `thread.list`, `thread.show`, `thread.create`, `thread.update`, `thread.resolve`, `thread.reopen` | Shared discussion records with TH-N keys |
| `task.reply/ask/answer/edit-post`, `thread.reply/ask/answer/edit-post` | Attributed discussion posts, with answers targeting a stable per-record P-N |
| `question.list`, `question.show`, `question.ask`, `question.update`, `question.answer`, `question.resolve`, `question.reopen` | Shared questions; answers and resolution changes produce attributed room events |
| `web.bootstrap` | Mint a one-use owner browser URL |

Unix requests and responses are newline-delimited JSON. Browser RPC uses the
same request envelope after authentication. Unknown input fields are rejected.

Board records live in `board_items`, keyed by room and kind with stable `T-N` /
`Q-N` numbers. `board_history` keeps attributed changes and references question
messages instead of copying their bodies. Every mutation has an operation ID;
`board_operations` stores its request fingerprint and result so an identical retry
cannot duplicate a question answer. Reusing the ID for different input fails.
Updates check an expected revision; independent answers may append concurrently,
but an answer that also resolves checks the revision. Record, history, and room
event changes commit together. Creator identity never limits editing rights.

The local CLI, owner browser, remote connector, and collaborator browser use
these same methods. Remote calls enforce room membership and derive the actor
from the authenticated binding/browser session, ignoring client actor claims.
Documents are pinned to validated room-local revisions. Worker handles are
validated against room participants/humans, independent of their directories.
Lists and history are bounded and paginated, loaded separately from room
snapshots. Assigned metadata edits produce compact linked room events. Discussion events
reuse the existing room stream, delivery batching, and acknowledgement cursors.

`board_entries` holds versioned snapshots of each discussion post. Answers
reference its stable per-record number, not the current record body or most recent
question. Answered question content/choices cannot be edited; ask a follow-up.
Human-input hints derive from unanswered question posts on tasks and threads.
Human answers settle human requests; agent contributions do not silently clear them.
Plain updates never create questions or inherit choices. Resolved threads reject
new posts and metadata edits until reopened. Task descriptions remain separate.

An additive, transactionally marked migration converts old question histories,
without publishing events or replacing Q-N links. Historical canonical text is
retained; unknown historical choice snapshots are not invented. Current unanswered
questions retain their choices. Show returns the latest 50 chronological posts,
an earlier-post cursor, and any unanswered prompts outside that page. Operation
replays derive post/answer state at the receipt's revision, not the current one.

After a board mutation, subscriber mentions are added transactionally to its existing
canonical event, excluding its author. Agent OPs, discussion posters, and task
assignees subscribe automatically; humans do not. Subscriptions are append-only,
revision-snapshotted record data. Tasks have exactly one assignee (creator by
default); threads have none. The legacy workers array mirrors that single assignee.
A metadata-only mutation creates one event if another subscriber exists.
Startup backfills existing creators, discussion actors, and legacy workers without
emitting events; multi-worker tasks keep the first listed worker as assignee.
This reuses mention-priority delivery without changing room visibility, permissions,
or self-exclusion. It does not create a second event for a discussion post, and
operation receipt replay cannot repeat the notification.

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
and advances the participant's acknowledgement through that cutoff. Its wrapper
contains room/range/recipient metadata and command references, not instructions
about when to answer, stay silent, or interpret room responsibilities. Ordinary
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
