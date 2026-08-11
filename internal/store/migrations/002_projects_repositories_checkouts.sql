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
