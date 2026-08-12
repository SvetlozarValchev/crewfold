# Projects and checkouts acceptance scenario

This black-box scenario proves the project/repository/checkout boundary through
the real built CLI, daemon, SQLite store, and Git executable:

- register a project from a standalone clone without changing repository files;
- add adjacent standalone clones and a linked worktree as distinct checkouts;
- group all four checkouts under one shared Git-history repository identity;
- reject a non-repository path without a partial project;
- detect a dirty checkout;
- retain a moved checkout under its durable ID and mark it unavailable;
- stop and restart the daemon, then recover all four checkout records.

Run it directly:

```sh
./test/scenarios/projects-checkouts/run.sh
```

Fake-runner component tests separately prove scoped behavior when Git is absent or
returns malformed output.
