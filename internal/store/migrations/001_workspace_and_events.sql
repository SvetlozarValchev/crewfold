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

CREATE INDEX events_workspace_sequence_idx
    ON events(workspace_id, sequence);

CREATE TRIGGER events_reject_update
BEFORE UPDATE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are immutable');
END;

CREATE TRIGGER events_reject_delete
BEFORE DELETE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are immutable');
END;

CREATE TABLE idempotency_keys (
    key TEXT PRIMARY KEY,
    command TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response_json TEXT NOT NULL CHECK (json_valid(response_json)),
    created_at TEXT NOT NULL
) STRICT;
