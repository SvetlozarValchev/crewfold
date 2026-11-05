# Testing

The gate is local and provider-free:

```sh
./scripts/check.sh
```

Board tests cover shared editing by different creators, quiet unassigned edits,
archive/restore, departed participants, stale revisions, concurrent answers,
answer-and-resolve, retry deduplication, canonical answer history, paginated
lists/history, document/worker room scoping, and authenticated collaborator
identity. Connector routing and CLI tests use the same room commands without
starting provider sessions. Browser review must keep Conversation as the default,
verify selected archive/resolved filters and counts, and exercise links from
question answers in chat back to the board.

Thread tests also cover task discussions, exact answer targets, fresh follow-up
choices, immutable answered prompts, human-input cycles, resolved read-only state,
paginated posts with older pending questions, revision-bounded retry receipts,
and idempotent legacy migration without new room events. Browser checks cover
the task-only archive notice, collapsed Done, thread cards/timelines, hidden
composer while a question is pending, stale-record warnings, Documents tab,
and narrow-screen layout. Use an isolated daemon; never post test activity to
live rooms or restart provider services.

Subscription tests cover automatic OP/poster subscriptions, single task assignees,
former assignees remaining subscribed, no human/self pings, mentions on every
mutation, one canonical event per activity, retry deduplication, room-scoped
handles, and silent, idempotent migration of existing records. CLI/browser checks
cover the single task assignee selector and absence of thread assignment controls.

The browser regression uses only a disposable daemon and Chrome profile:

```sh
./scripts/build-web.sh
test_build=$(mktemp -d)
./scripts/go.sh build -o "$test_build/crewfold" ./cmd/crewfold
node scripts/test-web-live.mjs "$test_build/crewfold"
```

Requires Node with native WebSocket and `google-chrome` (or `CHROME_BIN`). It
checks automatic thread updates, stable dialog identity/scroll position, draft
retention, older pages and large bursts, targeted answers, edited question
choices, metadata conflicts, and responsive typography. It retains screenshots
and disposable data under the printed temporary directory for visual review.

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
