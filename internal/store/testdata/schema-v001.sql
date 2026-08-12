-- Migration fixture: an empty database at schema version 1.
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

CREATE TRIGGER events_reject_update BEFORE UPDATE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are immutable');
END;

CREATE TRIGGER events_reject_delete BEFORE DELETE ON events
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

INSERT INTO workspaces(id, name, revision, created_at, updated_at, created_by, updated_by)
VALUES (
    'ws_00000000000000000000000000000001', 'fixture-workspace', 1,
    '2026-08-12T00:00:00Z', '2026-08-12T00:00:00Z', 'local-owner', 'local-owner'
);

INSERT INTO events(
    event_id, type, schema_version, occurred_at, recorded_at,
    actor_id, actor_type, workspace_id, entity_type, entity_id,
    entity_revision, correlation_id, causation_id, data_json
) VALUES (
    'evt_00000000000000000000000000000001', 'workspace.created', 1,
    '2026-08-12T00:00:00Z', '2026-08-12T00:00:00Z',
    'local-owner', 'human', 'ws_00000000000000000000000000000001',
    'workspace', 'ws_00000000000000000000000000000001', 1,
    'fixture-request', NULL, '{"name":"fixture-workspace"}'
);

INSERT INTO idempotency_keys(key, command, request_hash, response_json, created_at)
VALUES (
    'fixture-workspace-key', 'workspace.init',
    'fixture-request-hash',
    '{"workspace":{"id":"ws_00000000000000000000000000000001","name":"fixture-workspace","revision":1,"created_at":"2026-08-12T00:00:00Z","updated_at":"2026-08-12T00:00:00Z","created_by":"local-owner","updated_by":"local-owner"},"event_id":"evt_00000000000000000000000000000001","event_sequence":1}',
    '2026-08-12T00:00:00Z'
);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (1, '001_workspace_and_events.sql', '2026-08-12T00:00:00Z');

PRAGMA user_version = 1;
