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
exact room-event relay without invoking a model.

The release gate also performs a manual real-runtime probe with a temporary
daemon and named Herdr session: start Codex, observe onboarding in the actual
terminal, send a private owner prompt, relay an external room message, verify the
steward publishes back to the shared feed, stop, and restart fresh.

Visual review uses a fresh temporary daemon and representative room data. It must
check the empty state, populated conversation, participant context, unread labels,
room creation dialog, hosted-steward console and lifecycle controls, responsive
layout, and document reader.
