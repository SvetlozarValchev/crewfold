CREATE TABLE message_threads (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    project_id TEXT REFERENCES projects(id),
    task_id TEXT REFERENCES tasks(id),
    subject TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('open', 'closed')),
    revision INTEGER NOT NULL CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL
) STRICT;

CREATE INDEX message_threads_workspace_idx ON message_threads(workspace_id, updated_at, id);

CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    thread_id TEXT NOT NULL REFERENCES message_threads(id),
    project_id TEXT REFERENCES projects(id),
    task_id TEXT REFERENCES tasks(id),
    sender_type TEXT NOT NULL CHECK (sender_type IN ('owner', 'agent_run')),
    sender_id TEXT NOT NULL,
    sender_agent_id TEXT REFERENCES agents(id),
    sender_run_id TEXT REFERENCES runs(id),
    kind TEXT NOT NULL CHECK (kind IN ('inform', 'question', 'request', 'review_request', 'handoff', 'decision_notice', 'risk', 'conflict', 'approval_request')),
    body TEXT NOT NULL,
    artifact_ids_json TEXT NOT NULL CHECK (json_valid(artifact_ids_json)),
    reply_to_message_id TEXT REFERENCES messages(id),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX messages_thread_idx ON messages(thread_id, created_at, id);

CREATE TABLE message_recipients (
    message_id TEXT NOT NULL REFERENCES messages(id),
    recipient_agent_id TEXT NOT NULL REFERENCES agents(id),
    status TEXT NOT NULL CHECK (status IN ('queued', 'delivered', 'read', 'acknowledged')),
    queued_at TEXT NOT NULL,
    delivered_at TEXT,
    read_at TEXT,
    acknowledged_at TEXT,
    delivered_run_id TEXT REFERENCES runs(id),
    PRIMARY KEY (message_id, recipient_agent_id)
) STRICT;

CREATE INDEX message_recipients_inbox_idx ON message_recipients(recipient_agent_id, status, queued_at, message_id);

CREATE TABLE message_wake_jobs (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    message_id TEXT NOT NULL REFERENCES messages(id),
    recipient_agent_id TEXT NOT NULL REFERENCES agents(id),
    target_run_id TEXT NOT NULL REFERENCES runs(id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'leased', 'succeeded', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TEXT NOT NULL,
    lease_expires_at TEXT,
    diagnostic TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (message_id, recipient_agent_id, target_run_id)
) STRICT;

CREATE INDEX message_wake_jobs_queue_idx ON message_wake_jobs(status, available_at, sequence);
