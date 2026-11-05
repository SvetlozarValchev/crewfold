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
and archive the room. Fake-runtime tests cover exact compact room-event delivery
without invoking a model. They reject one-turn-per-event regressions, premature
cursor advancement after provider failure, and full-message prompt injection.

Codex delivery tests cover default CLI self-binding, persistent notification
cursors, CLI queue failures, and rebinding the same participant to a replacement
thread. They also reject one-turn-per-event regressions for ordinary bursts while
requiring direct mentions to bypass the batching delay. A gated real-runtime
probe uses a disposable open Codex thread to validate `codex queue` delivery
without touching an existing conversation.

Tailnet coverage runs against temporary loopback servers only. It exercises
network enablement, one-use enrollment, replay rejection, room membership
scoping, remote participant identity, owner-operation rejection, handle-takeover
rejection, durable delivery poll/report, and remote CLI routing. Connector
coverage also verifies idle startup,
concurrent agent configuration writes, and per-directory routing across multiple
hubs with colliding room slugs. Delivery tests verify fixed sequence cutoffs,
messages arriving during injection, own-message filtering, cursor-zero backlog
ordering, and refusal to acknowledge an oversized range.

Claude adapter tests use a fake CLI to verify session selection, ambiguous
working directories, restricted relay arguments, missing sessions, and relay
failure. They do not invoke a paid model or send to real agent sessions.

Visual review uses a fresh temporary daemon and representative room data. It must
check the empty state, populated conversation, participant context, unread labels,
provider delivery state, room creation dialog, responsive layout,
active-roster filtering, adjacent-message grouping, and GitHub-flavored
Markdown tables in both messages and the document reader. Repeated uploads of one
filename must appear as one document with navigable revisions. Archived logical
documents must move to the archived view, remain restorable, and become active
automatically when their publisher uploads a new revision.

Canonical store coverage must reject substantial unstructured participant
messages and document captions even when a caller bypasses the CLI, while
allowing bounded authenticated owner conversation and retaining long replaceable
participant context outside the chronological feed.
