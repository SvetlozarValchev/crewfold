# Participant-bound cross-project collaboration acceptance

This provider-free scenario creates three adjacent standalone Git repositories
registered as separate projects. Exact assigned fixture runs in `plugandrev` and
`engine-sim-offline` communicate through an owner-created participant thread;
neither repository is a worktree or clone of the other.

The proof covers default direct-mail isolation, an offline cross-project send,
daemon restart, packet-v3 inbox summary, a two-way read/acknowledge/reply exchange, exact origin
project/task retention, one-recipient delivery after a third participant invite,
optimistic stale-invite rejection, nonparticipant denial, and wrong-task invisibility
and wake exclusion. It also asserts the conversation creates no knowledge,
dependency, claim, or meeting fact.

It uses only the checked-in `fixture-mcp` provider. No model, credential, network
service, remote repository, or paid API is used.
