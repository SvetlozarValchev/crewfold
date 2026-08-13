-- name: InsertContextPacket :exec
INSERT INTO context_packets(
    id, workspace_id, project_id, task_id, agent_id, checkout_id, packet_json,
    content_hash, byte_size, created_at, created_by
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetWorkspaceContextPacket :one
SELECT packet_json, project_id, task_id, agent_id, checkout_id, content_hash, byte_size
FROM context_packets WHERE id = ? AND workspace_id = ?;

-- name: GetContextRepositoryFingerprint :one
SELECT fingerprint FROM repositories WHERE id = ? AND workspace_id = ?;

-- name: CountContextDependencies :one
SELECT COUNT(*) FROM task_dependencies WHERE task_id = ?;

-- name: ListContextDependencies :many
SELECT task.id, task.title, task.status, task.revision
FROM task_dependencies dependency JOIN tasks task ON task.id = dependency.depends_on_task_id
WHERE dependency.task_id = ? ORDER BY task.id;

-- name: CountContextDependents :one
SELECT COUNT(*) FROM task_dependencies WHERE depends_on_task_id = ?;

-- name: ListContextDependents :many
SELECT task.id, task.title, task.status, task.revision
FROM task_dependencies dependency JOIN tasks task ON task.id = dependency.task_id
WHERE dependency.depends_on_task_id = ? ORDER BY task.id LIMIT ?;

-- name: CountContextParticipantThreads :one
SELECT COUNT(*)
FROM message_threads thread JOIN thread_participants participant ON participant.thread_id = thread.id
WHERE thread.workspace_id = ? AND thread.kind = 'participant_bound' AND thread.status = 'open'
  AND participant.status = 'active' AND participant.agent_id = ?
  AND participant.project_id = ? AND participant.task_id = ?;

-- name: ListContextParticipantThreadIDs :many
SELECT thread.id
FROM message_threads thread JOIN thread_participants participant ON participant.thread_id = thread.id
WHERE thread.workspace_id = ? AND thread.kind = 'participant_bound' AND thread.status = 'open'
  AND participant.status = 'active' AND participant.agent_id = ?
  AND participant.project_id = ? AND participant.task_id = ?
ORDER BY crewfold_timestamp_key(thread.updated_at) DESC, thread.id ASC LIMIT ?;

-- name: GetContextEventCursor :one
SELECT CAST(COALESCE(MAX(sequence), 0) AS INTEGER) FROM events;

-- name: GetContextCheckWatchGrant :one
SELECT id, workspace_id, project_id, agent_id, agent_revision,
       max_pending, max_in_flight, COALESCE(expires_at, '') AS expires_at,
       content_sha256, status, revision, created_at, updated_at, created_by, updated_by
FROM check_watch_grants
WHERE workspace_id = ? AND id = ?;

-- name: ListContextCheckWatchGrantOperations :many
SELECT operation
FROM check_watch_grant_operations
WHERE grant_id = ?
ORDER BY ordinal;

-- name: ListContextCheckWatchGrantDefinitions :many
SELECT definition_id, definition_content_revision, definition_sha256
FROM check_watch_grant_definitions
WHERE grant_id = ?
ORDER BY ordinal;
