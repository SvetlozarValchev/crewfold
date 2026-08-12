DROP INDEX task_assignments_one_active_idx;
DROP INDEX task_assignments_agent_status_idx;
DROP INDEX task_dependencies_depends_idx;
DROP INDEX tasks_project_status_idx;

ALTER TABLE task_dependencies RENAME TO task_dependencies_before_runs;
ALTER TABLE task_assignments RENAME TO task_assignments_before_runs;
ALTER TABLE tasks RENAME TO tasks_before_runs;

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    objective_id TEXT REFERENCES objectives(id),
    title TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL CHECK (status IN ('ready', 'assigned', 'active', 'blocked', 'review', 'changes_requested', 'completed', 'failed', 'cancelled')),
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

INSERT INTO tasks SELECT * FROM tasks_before_runs;

CREATE TABLE task_dependencies (
    task_id TEXT NOT NULL REFERENCES tasks(id),
    depends_on_task_id TEXT NOT NULL REFERENCES tasks(id),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    PRIMARY KEY (task_id, depends_on_task_id),
    CHECK (task_id <> depends_on_task_id)
) STRICT;

INSERT INTO task_dependencies SELECT * FROM task_dependencies_before_runs;

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

INSERT INTO task_assignments SELECT * FROM task_assignments_before_runs;

DROP TABLE task_dependencies_before_runs;
DROP TABLE task_assignments_before_runs;
DROP TABLE tasks_before_runs;

CREATE UNIQUE INDEX task_assignments_one_active_idx
    ON task_assignments(task_id) WHERE status = 'active';
CREATE INDEX tasks_project_status_idx ON tasks(project_id, status, priority DESC, created_at, id);
CREATE INDEX task_dependencies_depends_idx ON task_dependencies(depends_on_task_id, task_id);
CREATE INDEX task_assignments_agent_status_idx ON task_assignments(agent_id, status);

CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    checkout_id TEXT NOT NULL REFERENCES checkouts(id),
    runtime TEXT NOT NULL,
    provider TEXT NOT NULL,
    scenario_name TEXT NOT NULL,
    scenario_json TEXT NOT NULL CHECK (json_valid(scenario_json)),
    placement_reasons_json TEXT NOT NULL CHECK (json_valid(placement_reasons_json)),
    status TEXT NOT NULL CHECK (status IN ('requested', 'starting', 'active', 'blocked', 'review', 'completed', 'start_failed', 'failed')),
    step_cursor INTEGER NOT NULL CHECK (step_cursor >= 0),
    runtime_handle TEXT,
    provider_handle TEXT,
    blocked_question TEXT,
    result_summary TEXT,
    failure_code TEXT,
    failure_message TEXT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE TABLE run_jobs (
    run_id TEXT PRIMARY KEY REFERENCES runs(id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'leased', 'complete')),
    available_at TEXT NOT NULL,
    lease_expires_at TEXT,
    attempts INTEGER NOT NULL CHECK (attempts >= 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE TABLE run_timeline (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL REFERENCES runs(id),
    kind TEXT NOT NULL,
    message TEXT,
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),
    recorded_at TEXT NOT NULL
) STRICT;

CREATE TABLE run_handoffs (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    summary TEXT NOT NULL,
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX runs_one_live_task_idx
    ON runs(task_id) WHERE status IN ('requested', 'starting', 'active', 'blocked');
CREATE INDEX runs_workspace_status_idx ON runs(workspace_id, status, created_at, id);
CREATE INDEX runs_agent_status_idx ON runs(agent_id, status);
CREATE INDEX runs_checkout_status_idx ON runs(checkout_id, status);
CREATE INDEX run_jobs_ready_idx ON run_jobs(status, available_at, run_id);
CREATE INDEX run_timeline_run_idx ON run_timeline(run_id, sequence);
