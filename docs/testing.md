# Testing

The gate is local and provider-free. On Linux:

```sh
./scripts/check.sh
```

On Windows:

```powershell
.\scripts\check.ps1
```

It verifies formatting, TypeScript, embedded web assets, `go vet`, all Go tests,
the race detector when supported, and a production build. Native Windows also
has a disposable provider-free workflow:

```powershell
.\scripts\test-windows-workflow.ps1
```

That script builds a fresh binary and uses isolated state and a unique named
pipe. It covers two participant directories, special characters, Unicode paths,
messaging, context, document round-trip, acknowledgement, graceful daemon
restart persistence, and archive, then removes its state.

The room store test
exercises the representative workflow: create a room, join two
independent working directories, publish context and messages, upload and verify
a Markdown document, observe unread state, acknowledge, reject handle spoofing,
and archive the room. A fake-runtime test covers hosted-steward lifecycle and
exact compact room-event relay without invoking a model. It rejects regressions
that repeat the full onboarding policy in every steward delivery, deliver each
line of a short burst as a separate turn, acknowledge a delta before its Codex
turn settles, or redeliver the room history immediately after onboarding.

Codex delivery tests cover default CLI self-binding, persistent notification
cursors, queued delivery, and rebinding the same participant to a replacement
thread. A gated real-runtime probe uses a disposable Codex thread to validate the
owner-local app-server WebSocket and turn injection without touching an existing
conversation.

The release gate also performs manual real-runtime probes with temporary
daemons and named Herdr sessions. Codex is exercised on Unix. Native Windows
uses `--runtime pi` and verifies Pi startup, onboarding, room publication,
status, stop, named-session deletion, and cleanup of the temporarily installed
Herdr Pi integration. External Pi delivery is checked in both directions: room
activity triggers the bound conversation, and the model publishes through the
`crewfold_send` tool.
Manager restart coverage verifies that a daemon restart resumes an initialized
steward's last Codex thread without repeating onboarding, forking an empty
conversation, or publishing another introduction.

Visual review uses a fresh temporary daemon and representative room data. It must
check the empty state, populated conversation, participant context, unread labels,
Codex delivery state, room creation dialog, hosted-steward console and lifecycle
controls, responsive layout, adjacent-message grouping, and GitHub-flavored
Markdown tables in both messages and the document reader. Repeated uploads of one
filename must appear as one document with navigable revisions.

Canonical store coverage must reject substantial unstructured messages and
document captions even when a caller bypasses the CLI, while retaining long
replaceable participant context outside the chronological feed.
