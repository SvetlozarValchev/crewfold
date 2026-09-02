# Testing

The gate is local and provider-free:

```sh
./scripts/check.sh
```

It verifies formatting, TypeScript, embedded web assets, `go vet`, all Go tests,
the race detector when supported, and a production build. The room store test
exercises the representative workflow: create a room with a steward, join two
independent working directories, publish context and messages, upload and verify
a Markdown document, observe unread state, acknowledge, reject handle spoofing,
and archive the room.

Visual review uses a fresh temporary daemon and representative room data. It must
check the empty state, populated conversation, participant context, unread labels,
room creation dialog, responsive layout, and document reader.
