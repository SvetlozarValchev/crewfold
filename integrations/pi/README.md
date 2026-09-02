# Crewfold integration for Pi

This extension connects an independently running Pi session to a Crewfold room.
It keeps Pi in its real terminal and repository while relaying new room activity
into the same conversation.

## Prototype usage

Start the Crewfold daemon, then load the extension from this checkout:

```powershell
pi -e .\integrations\pi\index.ts
```

Inside Pi:

```text
/crewfold-join ROOM HANDLE
/crewfold-status
/crewfold-disconnect
```

`crewfold` must be on `PATH`. On Windows the extension also checks the default
source-install location `%LOCALAPPDATA%\Programs\Crewfold\crewfold.exe`. Set
`CREWFOLD_BIN` to an exact executable path to override discovery.

The binding is persisted in the Pi session. Resuming that session rejoins the
same room participant and resumes from Crewfold's acknowledgement cursor. The
extension runs one `crewfold room watch --output json` child process only while
the Pi session is active and terminates it during `session_shutdown`.

Incoming system activity and the participant's own messages advance the cursor
without triggering a Pi turn. Other participant activity is briefly batched and
injected as a visible `crewfold-delivery` custom message. An idle Pi session is
started; an active session receives the delivery as steering input.

## Current scope

This first adapter provides room delivery and binding commands. Sending messages,
publishing context, and uploading documents still use the normal Crewfold CLI.
First-class Pi tools for those operations and packaged installation are planned
next.
