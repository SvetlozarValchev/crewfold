# Milestone review — Durable two-agent messaging

## Identity

- Milestone: `M8 — Durable two-agent messaging`
- Review status: `passed`
- Implementation commit: `bc6235d76ee45d98a94c8e01b024c69b9eb2299f`
- Reviewer: Crewfold local implementation gate
- Date: `2026-08-12`

## Demonstrable outcome

- User-visible capability: one authenticated agent run sends bounded durable mail
  to an offline agent; the daemon restarts; the recipient starts in an adjacent
  standalone clone with an inbox summary already in its immutable briefing, lists
  and reads the full message, acknowledges it, replies in the same thread, and
  completes its task. The original sender receives and acknowledges the reply and
  completes without using terminal output as communication authority.
- Acceptance scenario path: `test/scenarios/agent-messaging/run.sh`
- Exact command: `./scripts/check.sh`
- Expected result: formatting, vet, package, migration, protocol, race, and all
  nine capability-named built-binary scenarios pass; the final scenario prints
  `Durable agent messaging acceptance: PASS` without a model, credential, remote,
  network service, or Git remote.
- Observed result: passed on Linux/amd64 with Go 1.26.5. Exactly two immutable
  messages and one thread survived the restart. Both recipient records reached
  `acknowledged`. The reply's live-runtime wake attempt failed visibly while
  polling still delivered the durable message.

## Test evidence

| Suite | Command | Result | Evidence |
| --- | --- | --- | --- |
| Complete local gate | `./scripts/check.sh` | passed | Formatting, vet, all Go tests, race detector, and nine built-binary scenarios |
| Store/migration | `go test ./internal/store` | passed | Offline queueing, packet summary, delivery/read/ack transitions, reply, exact replay after recipient disable, changed-payload conflict, failed wake, project scope, recovery revalidation, and schema 7 migration |
| Domain | `go test ./internal/domain` | passed | A legacy context-packet v1 omits the v2 inbox while a new packet emits a bounded empty inbox |
| Protocol | `go test ./protocol` | passed | Unique valid schema IDs/references, v1/v2 context contracts, provider-neutral message methods, and both mailbox fixture contracts |
| Component | `go test ./internal/daemon ./internal/execution` | passed | Packet allowlist filtering, strict required message-tool inputs, MCP dispatch, reconnecting fixture client, bounded wake hook, and fixture validation |
| CLI | `go test ./internal/cli` | passed | Message send, artifact/reply/thread options, inbox inspection, and thread display parse into versioned local API calls |
| Black-box acceptance | Agent messaging scenario via check script | passed | Offline send, byte-equivalent inbox across daemon restart, two direct MCP agents, adjacent clone placement, request/reply, two acknowledgements, and explicit wake failure |
| Race | `go test -race ./...` via check script | passed | Concurrent daemon requests, SQLite queues, MCP reconnect, direct supervision, and earlier capabilities remain race-clean |
| Earlier capabilities | Eight earlier scenarios via check script | passed | Build, daemon, workspace, source, coordination, deterministic/direct execution, and scoped MCP remain green |
| Live conformance | N/A | passed by explicit exclusion | Herdr, real providers, credentials, remote services, and paid calls belong to later milestones |

## Failure proof

- Injected failures: a run tries to address `owner`; a run sends a 4097-byte body;
  a stable send key is reused with different content; an owner message for one
  project targets an agent whose only live run belongs to another project; a
  recovered/corrupt wake entry points that message at the wrong-project run; and
  the default direct runtime cannot wake an active sender.
- Injection seams/barriers: authenticated MCP arguments, domain command
  validation, sender-scoped command hashes, live-run wake selection, completion
  revalidation, and the injected runtime wake hook.
- Expected diagnosis and recovery: human/broadcast-like recipients are
  `denied_by_policy`; oversized content is `invalid_input`; conflicting retries do
  not append another message; cross-project runs receive no wake and cannot be
  marked delivered by a stale job; runtime wake failure leaves delivery queued
  with a bounded diagnostic; later inbox polling still delivers normally.
- Observed diagnosis and recovery: all outcomes matched. The black-box inbox held
  exactly one message after both denied probes and remained byte-equivalent across
  restart. Store tests observed no cross-project job, converted an injected stale
  job to `failed`, retained queued delivery, and recorded a project-scope
  diagnostic. The live reply recorded `message.wake_failed`, then reached
  acknowledgement through polling.

## Persistence and recovery

- Durable state introduced or changed: schema version 7 adds `message_threads`,
  immutable `messages`, mutable `message_recipients`, and a separate durable
  `message_wake_jobs` queue. New context packets use domain schema v2 for the
  bounded inbox snapshot.
- Atomic boundaries: creating a thread when needed, inserting one message and
  recipient, appending its audit facts, recording idempotency, and optionally
  enqueueing wake intent commit together. Wake effect and wake completion happen
  after that transaction. Read and acknowledgement each update recipient state,
  append events, and record run-scoped idempotency in one transaction.
- Restart/crash points tested: the requester sends while the recipient is offline;
  owner inspection captures queued JSON; the daemon stops and restarts while the
  requester process survives; the same query is byte-equivalent; then the
  recipient starts and consumes the one durable message. Startup also reclaims
  pending or expired leased wake work.
- Reconciliation outcome: no duplicate message or thread is created. A definite
  wake failure is terminal diagnostic state for that attempt, not delivery
  failure. A queued message remains available until an applicable run lists or
  transitions it.
- Migration fixture: representative schema-v4 state upgrades through 5, 6, and 7
  with its prior run/timeline facts intact. All four message tables are empty,
  proving Crewfold invents no communication or wake authority for old state.
- Backup/restore impact: SQLite now owns all message/thread/delivery/wake facts.
  A live backup still also needs direct-runtime state and the private node key.
  There is no down migration; rollback requires a compatible pre-upgrade backup.

## Security and autonomy

- New actions/capabilities: an authenticated live run can list only its applicable
  inbox, mark one of its messages read or acknowledged, send to one enabled agent,
  and reply within an existing participant thread. The owner can send and inspect
  but cannot mark recipient state on an agent's behalf.
- Scope: sender identity, project, task, and run come from the capability. A run
  cannot select them in message arguments, address itself, address a human or
  broadcast, attach another run's artifact, or extend a thread to new participants.
  Inbox and transition queries exclude messages scoped to another project.
- Bounds: bodies contain 1–4096 valid UTF-8 bytes, subjects 1–160 bytes, artifact
  lists at most 16 unique IDs, owner/agent inbox pages at most 50 items, packet
  summaries at most ten previews, and wake diagnostics at most 1024 bytes.
- Immutable authority: existing context-packet v1 records remain readable with
  their original hash/size and omit inbox/tool additions. MCP tool discovery and
  calls are intersected with the packet's recorded allowlist, so an upgraded
  daemon cannot grant mailbox authority to an old live run.
- Audit: thread creation, send, delivery, read, acknowledgement, wake success, and
  wake failure are bounded journal facts. Agent mutations use `agent_run` actors;
  wake outcomes use a subsystem actor. Tool audit excludes bodies and credentials.
- Same-UID limitation: the direct children and daemon still share one OS user.
  Only fixed trusted fixtures are supported; a malicious same-user process could
  discover the owner socket or private files. This milestone adds protocol least
  authority, not process containment.
- External side effects and human approval: no source mutation, prompt injection
  into an arbitrary runtime, remote message, push, merge, deployment, or message
  to a real person occurs. Such effects still require later policy and approval.

## Compatibility

- API/schema changes: additive local methods `message.send`, `inbox.list`, and
  `thread.show`; four additive MCP tools; message/thread/delivery result schemas;
  fixture mailbox controls; context-packet v2; and context build/show result v2.
  Preserved v1 packet and result schemas remain checked in.
- Storage changes: forward-only schema migration 6→7. Older binaries refuse the
  newer `user_version`; rollback requires a pre-upgrade backup.
- Adapter/runtime compatibility: message persistence is runtime-independent. The
  daemon exposes one bounded wake hook; the current direct fixture deliberately
  reports that live wake is unavailable. Herdr can implement the hook without
  changing mail identity or delivery semantics.
- Source-layout compatibility: the requester uses the primary checkout and the
  reviewer uses a separately registered adjacent standalone clone. No Git
  worktree relationship, directory naming convention, or shared `.git` metadata
  is assumed.
- Earlier milestone scenarios rerun: all eight passed unchanged.

## Known limitations and deferrals

- There is no functional live-runtime prompt/wake implementation yet. The hook is
  durable and bounded, but direct fixture mail is discovered by polling.
- Threads are open-only in the public surface and currently contain one recipient
  per message. Thread closing, group expansion, announcements, priorities,
  acknowledgement requirements, and delivery retries are deferred.
- Only fixed fixture agents use MCP. Herdr, Codex, Claude Code, arbitrary commands,
  and provider-native resume remain disabled.
- Context packets snapshot at run creation. Existing sessions do not yet receive
  message deltas or automatic briefing refresh.
- Claims, overlap detection, meetings, managers, canonical knowledge, RAG,
  outcome assessments, organization identity, cross-machine sync, and messages to
  people remain explicitly deferred.
- Message bodies remain SQLite text. Larger evidence must use bounded run
  artifacts today; content-addressed large artifact storage is future work.

## Repository hygiene

- Working tree clean after implementation acceptance: yes.
- No leaked processes/sockets/temp directories: yes; cleanup validates exact
  binary ownership and deletes only the scenario's isolated temporary tree.
- No paid/network call in default tests: yes.
- Documentation and schemas match behavior: yes.
- No milestone codes in executable artifact paths, test identifiers, fixture
  values, environment variables, or temporary names: yes. Milestone codes remain
  only in planning/history prose.
- No upstream Git remote created: yes.

## Decision

- Exit gate satisfied: `yes`.
- Waivers: none.
- Next milestone entry criteria met: `yes`.
- Next milestone: `M9 — Herdr runtime driver with fixture agent`.
