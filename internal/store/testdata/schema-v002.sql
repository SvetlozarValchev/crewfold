-- Migration fixture: representative data at schema version 2.
PRAGMA application_id = 0x43524644;

CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TEXT NOT NULL
) STRICT;

CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE TABLE events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL,
    schema_version INTEGER NOT NULL CHECK (schema_version > 0),
    occurred_at TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    entity_type TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    entity_revision INTEGER NOT NULL CHECK (entity_revision > 0),
    correlation_id TEXT NOT NULL,
    causation_id TEXT,
    data_json TEXT NOT NULL CHECK (json_valid(data_json))
) STRICT;

CREATE INDEX events_workspace_sequence_idx ON events(workspace_id, sequence);
CREATE TRIGGER events_reject_update BEFORE UPDATE ON events BEGIN SELECT RAISE(ABORT, 'events are immutable'); END;
CREATE TRIGGER events_reject_delete BEFORE DELETE ON events BEGIN SELECT RAISE(ABORT, 'events are immutable'); END;

CREATE TABLE idempotency_keys (
    key TEXT PRIMARY KEY,
    command TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response_json TEXT NOT NULL CHECK (json_valid(response_json)),
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    name TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    UNIQUE (workspace_id, name)
) STRICT;

CREATE TABLE repositories (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    fingerprint TEXT NOT NULL,
    object_format TEXT NOT NULL CHECK (object_format IN ('sha1', 'sha256')),
    root_commits_json TEXT NOT NULL CHECK (json_valid(root_commits_json)),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    UNIQUE (workspace_id, fingerprint)
) STRICT;

CREATE TABLE project_repositories (
    project_id TEXT NOT NULL REFERENCES projects(id),
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    attached_at TEXT NOT NULL,
    PRIMARY KEY (project_id, repository_id)
) STRICT;

CREATE TABLE checkouts (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id),
    repository_id TEXT NOT NULL REFERENCES repositories(id),
    path TEXT NOT NULL UNIQUE,
    write_mode TEXT NOT NULL CHECK (write_mode IN ('exclusive', 'claimed', 'shared', 'read_only')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    availability TEXT NOT NULL CHECK (availability IN ('available', 'unavailable')),
    checkout_kind TEXT NOT NULL CHECK (checkout_kind IN ('standalone', 'linked_worktree', 'unknown')),
    branch TEXT,
    head_commit TEXT,
    dirty INTEGER NOT NULL CHECK (dirty IN (0, 1)),
    git_dir TEXT,
    git_common_dir TEXT,
    observed_at TEXT NOT NULL,
    diagnostic_code TEXT,
    diagnostic TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE INDEX projects_workspace_idx ON projects(workspace_id, name);
CREATE INDEX repositories_workspace_idx ON repositories(workspace_id, fingerprint);
CREATE INDEX checkouts_project_idx ON checkouts(project_id, path);
CREATE INDEX checkouts_repository_idx ON checkouts(repository_id);

INSERT INTO workspaces VALUES (
    'ws_00000000000000000000000000000002', 'upgrade-fixture', 1,
    '2026-08-12T00:00:00Z', '2026-08-12T00:00:00Z', 'local-owner', 'local-owner'
);
INSERT INTO projects VALUES (
    'prj_00000000000000000000000000000002', 'ws_00000000000000000000000000000002',
    'fixture-project', 1, '2026-08-12T00:00:00Z', '2026-08-12T00:00:00Z',
    'local-owner', 'local-owner'
);
INSERT INTO repositories VALUES (
    'repo_00000000000000000000000000000002', 'ws_00000000000000000000000000000002',
    'git_2222222222222222222222222222222222222222222222222222222222222222',
    'sha1', '["1111111111111111111111111111111111111111"]', 1,
    '2026-08-12T00:00:00Z', '2026-08-12T00:00:00Z', 'local-owner', 'local-owner'
);
INSERT INTO project_repositories VALUES (
    'prj_00000000000000000000000000000002',
    'repo_00000000000000000000000000000002', '2026-08-12T00:00:00Z'
);
INSERT INTO checkouts VALUES (
    'co_00000000000000000000000000000002',
    'prj_00000000000000000000000000000002',
    'repo_00000000000000000000000000000002',
    '/fixture/world-engine-2', 'exclusive', 1, 'available', 'standalone', 'main',
    '2222222222222222222222222222222222222222', 0,
    '/fixture/world-engine-2/.git', '/fixture/world-engine-2/.git',
    '2026-08-12T00:00:00Z', NULL, NULL,
    '2026-08-12T00:00:00Z', '2026-08-12T00:00:00Z', 'local-owner', 'local-owner'
);

INSERT INTO events(
    event_id, type, schema_version, occurred_at, recorded_at, actor_id, actor_type,
    workspace_id, entity_type, entity_id, entity_revision, correlation_id,
    causation_id, data_json
) VALUES (
    'evt_00000000000000000000000000000002', 'workspace.created', 1,
    '2026-08-12T00:00:00Z', '2026-08-12T00:00:00Z', 'local-owner', 'human',
    'ws_00000000000000000000000000000002', 'workspace',
    'ws_00000000000000000000000000000002', 1, 'fixture-request', NULL,
    '{"name":"upgrade-fixture"}'
);

INSERT INTO schema_migrations VALUES (1, '001_workspace_and_events.sql', '2026-08-12T00:00:00Z');
INSERT INTO schema_migrations VALUES (2, '002_projects_repositories_checkouts.sql', '2026-08-12T00:00:00Z');
PRAGMA user_version = 2;
