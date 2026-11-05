# Architecture

Crewfold has one owner-local Go hub binary and one restricted collaborator
connector built from the same provider-adapter implementation:

```text
external Codex -- CLI -- owner Unix socket -- room store (SQLite + document files)
      ^                               |
      +-------- codex queue ----------+ room-delivery manager
                                      |
human browser -- loopback HTTP -------+--- hosted-steward manager --- named Herdr/Codex session
                                      |
remote connector -- tailnet HTTPS -- Tailscale Serve -- authenticated network API
      |                                                        |
      +-- connector service -- local codex queue                |
      +-- local Claude Channel MCP -----------------------------+
      +-- scoped human browser session -------------------------+
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
| `participant.join`, `participant.leave`, `participant.ack` | Bind or deactivate a working-directory participant; advance its read cursor |
| `message.send` | Append a message or publish current context |
| `document.upload`, `document.read` | Share and verify immutable document bytes |
| `document.archive`, `document.restore` | Hide or restore one logical publisher-and-filename document without deleting revisions |
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

## Tailnet method surface

The optional network API is served from the same loopback HTTP listener under
`/api/network/*`; Tailscale Serve terminates tailnet HTTPS and forwards to that
listener. It is disabled until the owner records one HTTPS public origin. The
owner RPC remains origin-checked and requires its separate local credential.

Enrollment consumes a single-use, room-scoped invitation and returns one random
device bearer token. Only a SHA-256 digest is stored. The authenticated remote
RPC admits room listing and snapshots for accepted memberships, participant
join/leave, messages, acknowledgements, document operations, and delivery
poll/report. It does not dispatch owner methods. For participant mutations the
server derives the actor from a binding owned by the authenticated device and
discards caller-supplied owner identity.

An authenticated connector can mint a one-use, two-minute browser bootstrap.
It becomes a persistent hashed browser session bound to the same device room
memberships. Its RPC allowlist contains room listing and snapshots, document
reads, human messages, and one immutable human-handle setup operation. Owner
methods are rejected server-side. The device's human handle is associated with
its network participant bindings; local participants are associated with
`@owner`, while hosted stewards have no human-owner field.

Each remote participant binding records its provider and a durable hub delivery
cursor. Provider targets remain in mode-`0600` configuration on the remote
machine. One connector service long-polls every enrolled hub with bounded
requests and sends each Codex binding's room batches through the remote
machine's `codex queue` command. It is installed before any room is selected and
remains healthy with no bindings. Hub credentials and agent bindings are
independent, so one OS user can participate across multiple Crewfold servers.
The Claude adapter is an MCP stdio server
with the experimental `claude/channel` capability. Both acknowledge only the
portion of a bounded batch actually emitted, so compact prompt limits cannot
skip canonical room events.

## Runtime boundary

External participants retain their native context and working environment.
Codex participants may opt into one narrow runtime adapter: `room join` binds
the current `CODEX_THREAD_ID` and verifies that the local CLI supports
`codex queue`. The delivery manager sends exact new room events to that open
conversation with `codex queue --thread ... --message ...`. It batches ordinary
activity after a short quiet period, flushes continuously busy rooms at a
bounded maximum delay, and bypasses that delay for a direct participant mention
or `@everyone` broadcast. The compact delivery envelope does not repeat the full
CLI manual or claim that silence is always appropriate: the participant decides
whether an event addresses it directly, collectively, or through its room role.
The manager never passes `--resume`, starts external terminals, or captures
provider transcripts. The same stable participant can rebind to a new open
thread by joining again.

Non-Codex tools and manual scripts join with `--delivery none` and use the CLI
feed directly. Delivery cursors and acknowledgement cursors are separate durable
facts.

For a tailnet participant, the same runtime boundary is split deliberately: the
hub owns room authorization and pending delivery; the collaborator machine owns
the provider target and performs the final injection. A remote Codex thread ID
or Claude session identifier is never sent to the hub. The complete deployment
and security boundary is in [tailnet collaboration](tailnet.md).

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
onboarding, or publish another introduction. If a manually resumed Codex process
loses Herdr's friendly agent name, Crewfold verifies the recorded pane, agent kind,
and working directory before restoring that name automatically.
