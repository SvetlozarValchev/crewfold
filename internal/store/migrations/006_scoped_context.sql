CREATE TABLE context_packets (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT NOT NULL REFERENCES projects(id),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    agent_id TEXT NOT NULL REFERENCES agents(id),
    checkout_id TEXT NOT NULL REFERENCES checkouts(id),
    packet_json TEXT NOT NULL CHECK (json_valid(packet_json)),
    content_hash TEXT NOT NULL,
    byte_size INTEGER NOT NULL CHECK (byte_size > 0 AND byte_size <= 32768),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL
) STRICT;

CREATE INDEX context_packets_task_idx ON context_packets(task_id, created_at, id);

CREATE TABLE run_context_bindings (
    run_id TEXT PRIMARY KEY REFERENCES runs(id),
    context_packet_id TEXT NOT NULL UNIQUE REFERENCES context_packets(id),
    bound_at TEXT NOT NULL
) STRICT;

CREATE TABLE run_capabilities (
    run_id TEXT PRIMARY KEY REFERENCES runs(id),
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE TABLE run_reports (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    run_id TEXT NOT NULL REFERENCES runs(id),
    kind TEXT NOT NULL CHECK (kind IN ('progress', 'blocked', 'completion')),
    message TEXT NOT NULL,
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),
    handoff TEXT,
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'applied')),
    created_at TEXT NOT NULL,
    applied_at TEXT,
    UNIQUE (run_id, idempotency_key)
) STRICT;

CREATE INDEX run_reports_pending_idx ON run_reports(run_id, status, sequence);

CREATE TABLE run_artifacts (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    name TEXT NOT NULL,
    media_type TEXT NOT NULL,
    content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    byte_size INTEGER NOT NULL CHECK (byte_size >= 0 AND byte_size <= 32768),
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (run_id, idempotency_key)
) STRICT;

CREATE INDEX run_artifacts_run_idx ON run_artifacts(run_id, created_at, id);

CREATE TABLE run_tool_calls (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs(id),
    request_id TEXT NOT NULL,
    method TEXT NOT NULL,
    target_id TEXT,
    outcome TEXT NOT NULL CHECK (outcome IN ('allowed', 'denied', 'error')),
    error_code TEXT,
    recorded_at TEXT NOT NULL
) STRICT;

CREATE INDEX run_tool_calls_run_idx ON run_tool_calls(run_id, recorded_at, id);
