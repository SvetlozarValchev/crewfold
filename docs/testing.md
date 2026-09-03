# Testing

The gate is local and provider-free:

```sh
./scripts/check.sh
```

It verifies formatting, TypeScript, embedded web assets, `go vet`, all Go tests,
the race detector when supported, and a production build. The room store test
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

The release gate also performs a manual real-runtime probe with a temporary
daemon and named Herdr session: start Codex, observe onboarding in the actual
terminal, send a private owner prompt, relay a direct participant exchange and
verify that the quiet steward takes no public action, explicitly address the
steward and verify one useful response, then stop and restart fresh.
Manager restart coverage verifies that a daemon restart resumes an initialized
steward without repeating onboarding or publishing another introduction.

Visual review uses a fresh temporary daemon and representative room data. It must
check the empty state, populated conversation, participant context, unread labels,
Codex delivery state, room creation dialog, hosted-steward console and lifecycle
controls, responsive layout, adjacent-message grouping, and GitHub-flavored
Markdown tables in both messages and the document reader. Repeated uploads of one
filename must appear as one document with navigable revisions.

Canonical store coverage must reject substantial unstructured messages and
document captions even when a caller bypasses the CLI, while retaining long
replaceable participant context outside the chronological feed.
