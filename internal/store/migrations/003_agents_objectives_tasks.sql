CREATE TABLE agents (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    name TEXT NOT NULL,
    role TEXT NOT NULL,
    provider TEXT NOT NULL,
    runtime TEXT NOT NULL,
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    max_concurrency INTEGER NOT NULL CHECK (max_concurrency > 0),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    UNIQUE (workspace_id, name)
) STRICT;

CREATE TABLE objectives (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'completed', 'cancelled')),
    budget_tokens INTEGER NOT NULL CHECK (budget_tokens >= 0),
    budget_cost_cents INTEGER NOT NULL CHECK (budget_cost_cents >= 0),
    budget_time_seconds INTEGER NOT NULL CHECK (budget_time_seconds >= 0),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    objective_id TEXT REFERENCES objectives(id),
    title TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL CHECK (status IN ('ready', 'assigned', 'active', 'blocked', 'completed', 'cancelled')),
    blocked_reason TEXT,
    priority INTEGER NOT NULL CHECK (priority BETWEEN 0 AND 1000),
    budget_tokens INTEGER NOT NULL CHECK (budget_tokens >= 0),
    budget_cost_cents INTEGER NOT NULL CHECK (budget_cost_cents >= 0),
    budget_time_seconds INTEGER NOT NULL CHECK (budget_time_seconds >= 0),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE TABLE task_dependencies (
    task_id TEXT NOT NULL REFERENCES tasks(id),
    depends_on_task_id TEXT NOT NULL REFERENCES tasks(id),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    PRIMARY KEY (task_id, depends_on_task_id),
    CHECK (task_id <> depends_on_task_id)
) STRICT;

CREATE TABLE task_assignments (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    status TEXT NOT NULL CHECK (status IN ('active', 'expired', 'released')),
    lease_expires_at TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX task_assignments_one_active_idx
    ON task_assignments(task_id) WHERE status = 'active';
CREATE INDEX agents_workspace_idx ON agents(workspace_id, name);
CREATE INDEX objectives_project_idx ON objectives(project_id, status);
CREATE INDEX tasks_project_status_idx ON tasks(project_id, status, priority DESC, created_at, id);
CREATE INDEX task_dependencies_depends_idx ON task_dependencies(depends_on_task_id, task_id);
CREATE INDEX task_assignments_agent_status_idx ON task_assignments(agent_id, status);
