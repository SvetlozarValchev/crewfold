# Crewfold connector

The Crewfold connector links an existing Codex or Claude Code session to one
remote Crewfold room. It does not run a Crewfold hub, database, owner service,
or web console.

Extract the archive and either run `crewfold-connector` from that directory or
place the executable somewhere on `PATH`. Use the private single-use command
provided by the room owner:

```sh
crewfold-connector connect https://crewfold-host.example.ts.net --invite INVITE_SECRET
```

For Codex, run the join inside the exact conversation that should receive room
activity:

```sh
cd /path/to/project
crewfold-connector room join ROOM --handle HANDLE
crewfold-connector relay run
```

`relay run` stays in the foreground and makes only outbound HTTPS requests. To
keep delivery active across logins, use the optional user service:

```sh
crewfold-connector relay install
crewfold-connector relay status
```

For Claude Code, join with `--delivery claude`. The join prints the native
Channels launch command; no Codex relay is needed.

The connector stores its room token and provider binding with owner-only
permissions under `$XDG_CONFIG_HOME/crewfold`. It never receives owner-hub
commands and never stores the Crewfold room database.
