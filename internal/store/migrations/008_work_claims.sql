ALTER TABLE checkouts ADD COLUMN dirty_paths_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(dirty_paths_json));

CREATE TABLE work_claims (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    checkout_id TEXT REFERENCES checkouts(id),
    kind TEXT NOT NULL CHECK (kind IN ('path', 'component', 'operation')),
    target TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('exclusive', 'shared', 'advisory')),
    conflict_policy TEXT NOT NULL CHECK (conflict_policy IN ('notify', 'deny_new', 'pause_scheduling', 'request_resolution')),
    status TEXT NOT NULL CHECK (status IN ('active', 'expired', 'released')),
    baseline_paths_json TEXT NOT NULL CHECK (json_valid(baseline_paths_json)),
    lease_expires_at TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX work_claims_active_scope_idx
    ON work_claims(task_id, kind, target, COALESCE(checkout_id, '')) WHERE status = 'active';
CREATE INDEX work_claims_project_status_idx
    ON work_claims(project_id, status, kind, lease_expires_at, id);
CREATE INDEX work_claims_checkout_status_idx
    ON work_claims(checkout_id, status, task_id, id);

CREATE TABLE work_overlaps (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    claim_low_id TEXT NOT NULL REFERENCES work_claims(id),
    claim_high_id TEXT NOT NULL REFERENCES work_claims(id),
    task_low_id TEXT NOT NULL REFERENCES tasks(id),
    task_high_id TEXT NOT NULL REFERENCES tasks(id),
    kind TEXT NOT NULL CHECK (kind IN ('path', 'component', 'operation')),
    witness TEXT NOT NULL,
    severity TEXT NOT NULL CHECK (severity IN ('critical', 'high', 'medium', 'low')),
    policy_response TEXT NOT NULL CHECK (policy_response IN ('notify', 'deny_new', 'pause_scheduling', 'request_resolution')),
    scheduling_paused INTEGER NOT NULL CHECK (scheduling_paused IN (0, 1)),
    resolution_required INTEGER NOT NULL CHECK (resolution_required IN (0, 1)),
    status TEXT NOT NULL CHECK (status IN ('open', 'resolved')),
    explanation_json TEXT NOT NULL CHECK (json_valid(explanation_json)),
    detected_at TEXT NOT NULL,
    resolved_at TEXT,
    resolution_reason TEXT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    UNIQUE (claim_low_id, claim_high_id),
    CHECK (claim_low_id < claim_high_id),
    CHECK (task_low_id <> task_high_id)
) STRICT;

CREATE INDEX work_overlaps_workspace_status_idx
    ON work_overlaps(workspace_id, status, severity, detected_at, id);
CREATE INDEX work_overlaps_project_status_idx
    ON work_overlaps(project_id, status, detected_at, id);

CREATE TABLE task_coordination_holds (
    overlap_id TEXT NOT NULL REFERENCES work_overlaps(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    reason TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (overlap_id, task_id)
) STRICT;

CREATE INDEX task_coordination_holds_task_idx ON task_coordination_holds(task_id, overlap_id);

CREATE TABLE claim_drifts (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    claim_id TEXT NOT NULL REFERENCES work_claims(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    checkout_id TEXT NOT NULL REFERENCES checkouts(id),
    path TEXT NOT NULL,
    head_commit TEXT,
    observation_gap INTEGER NOT NULL CHECK (observation_gap IN (0, 1)),
    status TEXT NOT NULL CHECK (status IN ('open', 'resolved')),
    first_observed_at TEXT NOT NULL,
    last_observed_at TEXT NOT NULL,
    resolved_at TEXT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    UNIQUE (task_id, checkout_id, path)
) STRICT;

CREATE INDEX claim_drifts_workspace_status_idx
    ON claim_drifts(workspace_id, status, last_observed_at, id);
CREATE INDEX claim_drifts_claim_status_idx
    ON claim_drifts(claim_id, status, path);

CREATE TABLE checkout_claim_scans (
    checkout_id TEXT PRIMARY KEY REFERENCES checkouts(id),
    watcher_id TEXT NOT NULL,
    head_commit TEXT,
    dirty_paths_json TEXT NOT NULL CHECK (json_valid(dirty_paths_json)),
    observed_at TEXT NOT NULL
) STRICT;
