-- name: InsertRunContextDeltaState :exec
INSERT INTO run_context_delta_state(
    run_id, context_packet_id, status, revision, scan_event_sequence,
    last_sequence, last_delta_id, pending_delta_id, last_acknowledged_delta_id,
    delta_count, cumulative_byte_size, rebase_reason, rebase_event_sequence,
    created_at, updated_at
) VALUES (?, ?, 'ready', 1, ?, 0, NULL, NULL, NULL, 0, 0, NULL, NULL, ?, ?);

-- name: GetRunContextDeltaState :one
SELECT run_id, context_packet_id, status, revision, scan_event_sequence,
       last_sequence, last_delta_id, pending_delta_id,
       last_acknowledged_delta_id, delta_count, cumulative_byte_size,
       rebase_reason, rebase_event_sequence, created_at, updated_at
FROM run_context_delta_state
WHERE run_id = ?;

-- name: InsertContextDelta :exec
INSERT INTO context_deltas(
    id, run_id, context_packet_id, sequence, parent_delta_id,
    from_event_sequence, through_event_sequence, delta_json, content_hash,
    byte_size, built_event_sequence, created_at, created_by
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: AdvanceRunContextDeltaScan :execrows
UPDATE run_context_delta_state
SET revision = revision + 1, scan_event_sequence = ?, updated_at = ?
WHERE run_id = ? AND context_packet_id = ? AND status = 'ready'
  AND revision = ? AND scan_event_sequence = ? AND pending_delta_id IS NULL;

-- name: MarkRunContextDeltaPending :execrows
UPDATE run_context_delta_state
SET status = 'pending_ack', revision = revision + 1,
    scan_event_sequence = ?, last_sequence = ?, last_delta_id = ?,
    pending_delta_id = ?, delta_count = delta_count + 1,
    cumulative_byte_size = cumulative_byte_size + ?, updated_at = ?
WHERE run_id = ? AND context_packet_id = ? AND status = 'ready'
  AND revision = ? AND scan_event_sequence = ? AND last_sequence = ?
  AND pending_delta_id IS NULL;

-- name: MarkRunContextRebaseRequired :execrows
UPDATE run_context_delta_state
SET status = 'rebase_required', revision = revision + 1,
    scan_event_sequence = ?, rebase_reason = ?, rebase_event_sequence = ?, updated_at = ?
WHERE run_id = ? AND context_packet_id = ? AND status = 'ready'
  AND revision = ? AND scan_event_sequence = ? AND pending_delta_id IS NULL;

-- name: InsertContextDeltaAcknowledgement :exec
INSERT INTO context_delta_acknowledgements(
    id, run_id, context_packet_id, delta_id, sequence,
    acknowledged_at, acknowledged_by, idempotency_key, request_hash, event_sequence
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: AcknowledgeRunContextDelta :execrows
UPDATE run_context_delta_state
SET status = 'ready', revision = revision + 1, pending_delta_id = NULL,
    last_acknowledged_delta_id = ?, updated_at = ?
WHERE run_id = ? AND context_packet_id = ? AND status = 'pending_ack'
  AND revision = ? AND pending_delta_id = ? AND last_sequence = ?;

-- name: GetContextDeltaByID :one
SELECT id, run_id, context_packet_id, sequence, parent_delta_id,
       from_event_sequence, through_event_sequence, delta_json, content_hash,
       byte_size, built_event_sequence, created_at, created_by
FROM context_deltas
WHERE id = ?;

-- name: GetWorkspaceContextDeltaByID :one
SELECT delta.id, delta.run_id, delta.context_packet_id, delta.sequence, delta.parent_delta_id,
       delta.from_event_sequence, delta.through_event_sequence, delta.delta_json, delta.content_hash,
       delta.byte_size, delta.built_event_sequence, delta.created_at, delta.created_by
FROM context_deltas delta
JOIN runs run ON run.id = delta.run_id
WHERE delta.id = ? AND run.workspace_id = ?;

-- name: GetRunContextDeltaByID :one
SELECT id, run_id, context_packet_id, sequence, parent_delta_id,
       from_event_sequence, through_event_sequence, delta_json, content_hash,
       byte_size, built_event_sequence, created_at, created_by
FROM context_deltas
WHERE id = ? AND run_id = ?;

-- name: ListRunContextDeltas :many
SELECT id, run_id, context_packet_id, sequence, parent_delta_id,
       from_event_sequence, through_event_sequence, delta_json, content_hash,
       byte_size, built_event_sequence, created_at, created_by
FROM context_deltas
WHERE run_id = ? AND sequence > ?
ORDER BY sequence
LIMIT ?;

-- name: ListAllRunContextDeltas :many
SELECT id, run_id, context_packet_id, sequence, parent_delta_id,
       from_event_sequence, through_event_sequence, delta_json, content_hash,
       byte_size, built_event_sequence, created_at, created_by
FROM context_deltas
WHERE run_id = ?
ORDER BY sequence;

-- name: GetContextDeltaAcknowledgement :one
SELECT id, run_id, context_packet_id, delta_id, sequence,
       acknowledged_at, acknowledged_by, idempotency_key, request_hash, event_sequence
FROM context_delta_acknowledgements
WHERE delta_id = ? AND run_id = ?;
