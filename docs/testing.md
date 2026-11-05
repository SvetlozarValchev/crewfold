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
cursors, CLI queue failures, and rebinding the same participant to a replacement
thread. They also reject one-turn-per-event regressions for ordinary bursts while
requiring direct mentions to bypass the batching delay. A gated real-runtime
probe uses a disposable open Codex thread to validate `codex queue` delivery
without touching an existing conversation.

Tailnet coverage runs against temporary loopback servers only. It exercises
network enablement, one-use enrollment, replay rejection, room membership
scoping, remote participant identity, owner-operation rejection, handle-takeover
rejection, durable delivery poll/report, remote CLI routing, and the Claude
Channel capability and tools. Connector coverage also verifies idle startup,
concurrent agent configuration writes, and per-directory routing across multiple
hubs with colliding room slugs. Prompt-size coverage verifies that delivery never
acknowledges canonical messages omitted from a compact provider notification.

The release gate also performs a manual real-runtime probe with a temporary
daemon and named Herdr session: start Codex, observe onboarding in the actual
terminal, send a private owner prompt, relay a direct participant exchange and
verify that the quiet steward takes no public action, explicitly address the
steward and verify one useful response, then stop and restart fresh.
Manager restart coverage verifies that a daemon restart resumes an initialized
steward's last Codex thread without repeating onboarding, forking an empty
conversation, or publishing another introduction.

Visual review uses a fresh temporary daemon and representative room data. It must
check the empty state, populated conversation, participant context, unread labels,
Codex delivery state, room creation dialog, hosted-steward console and lifecycle
controls, responsive layout, adjacent-message grouping, and GitHub-flavored
Markdown tables in both messages and the document reader. Repeated uploads of one
filename must appear as one document with navigable revisions. Archived logical
documents must move to the archived view, remain restorable, and become active
automatically when their publisher uploads a new revision.

Canonical store coverage must reject substantial unstructured participant
messages and document captions even when a caller bypasses the CLI, while
allowing bounded authenticated owner conversation and retaining long replaceable
participant context outside the chronological feed.
