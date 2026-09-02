# CLI reference

## Service and web

```text
crewfold service install|start|stop|status
crewfold open
crewfold status
```

`service install` writes and starts the owner systemd user service. `open` mints a
one-time browser URL from the private Unix socket.

## Rooms

```text
crewfold room create SLUG [--title TITLE] [--topic TOPIC]
crewfold room list
crewfold room show ROOM
crewfold room archive ROOM
```

## Participants and conversation

Run participant commands from the real agent's working directory:

```text
crewfold room join ROOM --handle HANDLE [--name NAME] [--kind agent|steward] [--delivery codex|none]
crewfold room send ROOM MESSAGE...
crewfold room context ROOM CURRENT-CONTEXT...
crewfold room read ROOM [--after SEQUENCE]
crewfold room watch ROOM [--after SEQUENCE]
crewfold room ack ROOM [--through SEQUENCE]
```

Codex delivery is the default. Run `join` from inside the Codex session; Crewfold
reads `CODEX_THREAD_ID`, validates it through Codex app-server, and binds later
notifications to that durable thread. `--delivery none` explicitly creates or
rebinds a manual participant without an injection target.

`read` and `watch` acknowledge through the observed cursor when the current
directory is a participant. `watch` remains a manual/debugging stream; a bound
Codex participant does not need to poll.

## Documents

```text
crewfold room upload ROOM FILE [--caption TEXT]
crewfold room document ROOM DOCUMENT [--to PATH]
```

`DOCUMENT` may be the document ID or its exact name. Standard non-streaming
commands accept `--output json`.

## Hosted room steward

```text
crewfold room steward start ROOM --handle HANDLE [--name NAME] [--role ROLE] [--cwd PATH]
crewfold room steward status ROOM
crewfold room steward prompt ROOM MESSAGE...
crewfold room steward key ROOM enter|esc|ctrl+c
crewfold room steward stop ROOM
crewfold room steward restart ROOM
```

With no `--cwd`, Crewfold creates a private room workspace and runs the hosted
steward non-interactively with local access to Crewfold's Unix socket. A custom
directory retains Codex's normal trust and approval prompts. `status --output
json` includes bounded real terminal scrollback. `restart` starts a fresh
Herdr/Codex session while preserving the room participant and shared history.

`CREWFOLD_SOCKET` selects a daemon socket for any CLI command. An explicit
`--socket` still takes precedence.
