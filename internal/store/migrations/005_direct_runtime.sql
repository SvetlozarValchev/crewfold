ALTER TABLE run_handoffs RENAME TO run_handoffs_before_direct_runtime;
ALTER TABLE run_timeline RENAME TO run_timeline_before_direct_runtime;
ALTER TABLE run_jobs RENAME TO run_jobs_before_direct_runtime;
ALTER TABLE runs RENAME TO runs_before_direct_runtime;

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
    status TEXT NOT NULL CHECK (status IN ('requested', 'starting', 'active', 'blocked', 'stopping', 'stopped', 'lost', 'review', 'completed', 'start_failed', 'failed')),
    step_cursor INTEGER NOT NULL CHECK (step_cursor >= 0),
    runtime_handle TEXT,
    provider_handle TEXT,
    blocked_question TEXT,
    result_summary TEXT,
    failure_code TEXT,
    failure_message TEXT,
    stop_grace_millis INTEGER NOT NULL DEFAULT 0 CHECK (stop_grace_millis >= 0 AND stop_grace_millis <= 30000),
    stop_forced INTEGER NOT NULL DEFAULT 0 CHECK (stop_forced IN (0, 1)),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

INSERT INTO runs(
    id, workspace_id, project_id, task_id, agent_id, checkout_id, runtime, provider,
    scenario_name, scenario_json, placement_reasons_json, status, step_cursor,
    runtime_handle, provider_handle, blocked_question, result_summary, failure_code,
    failure_message, stop_grace_millis, stop_forced, revision, created_at, updated_at, started_at,
    finished_at, created_by, updated_by
)
SELECT
    id, workspace_id, project_id, task_id, agent_id, checkout_id, runtime, provider,
    scenario_name, scenario_json, placement_reasons_json, status, step_cursor,
    runtime_handle, provider_handle, blocked_question, result_summary, failure_code,
    failure_message, 0, 0, revision, created_at, updated_at, started_at, finished_at,
    created_by, updated_by
FROM runs_before_direct_runtime;

CREATE TABLE run_jobs (
    run_id TEXT PRIMARY KEY REFERENCES runs(id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'leased', 'complete')),
    available_at TEXT NOT NULL,
    lease_expires_at TEXT,
    attempts INTEGER NOT NULL CHECK (attempts >= 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

INSERT INTO run_jobs SELECT * FROM run_jobs_before_direct_runtime;

CREATE TABLE run_timeline (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL REFERENCES runs(id),
    kind TEXT NOT NULL,
    message TEXT,
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),
    recorded_at TEXT NOT NULL
) STRICT;

INSERT INTO run_timeline SELECT * FROM run_timeline_before_direct_runtime;

CREATE TABLE run_handoffs (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    summary TEXT NOT NULL,
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL
) STRICT;

INSERT INTO run_handoffs SELECT * FROM run_handoffs_before_direct_runtime;

DROP TABLE run_handoffs_before_direct_runtime;
DROP TABLE run_timeline_before_direct_runtime;
DROP TABLE run_jobs_before_direct_runtime;
DROP TABLE runs_before_direct_runtime;

CREATE UNIQUE INDEX runs_one_live_task_idx
    ON runs(task_id) WHERE status IN ('requested', 'starting', 'active', 'blocked', 'stopping', 'lost');
CREATE INDEX runs_workspace_status_idx ON runs(workspace_id, status, created_at, id);
CREATE INDEX runs_agent_status_idx ON runs(agent_id, status);
CREATE INDEX runs_checkout_status_idx ON runs(checkout_id, status);
CREATE INDEX run_jobs_ready_idx ON run_jobs(status, available_at, run_id);
CREATE INDEX run_timeline_run_idx ON run_timeline(run_id, sequence);
