-- name: InsertKnowledgeContradiction :exec
INSERT INTO knowledge_contradictions(
    id, workspace_id, project_id, left_revision_id, right_revision_id,
    status, state_revision, report_note, reported_at, reported_by,
    reported_by_type, detected_event_sequence
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(project_id),
    sqlc.arg(left_revision_id), sqlc.arg(right_revision_id), 'proposed', 1,
    sqlc.arg(report_note), sqlc.arg(reported_at), sqlc.arg(reported_by),
    sqlc.arg(reported_by_type), sqlc.arg(detected_event_sequence)
);

-- name: GetKnowledgeContradiction :one
SELECT id, workspace_id, project_id, left_revision_id, right_revision_id,
       status, state_revision, report_note, reported_at, reported_by,
       reported_by_type, detected_event_sequence,
       confirmed_at, confirmed_by, confirmed_by_type, confirm_note, confirm_event_sequence,
       dismissed_at, dismissed_by, dismissed_by_type, dismiss_note, dismiss_event_sequence,
       resolution_reason, resolved_at, resolved_by, resolved_by_type, resolution_note,
       resolution_event_sequence, resolution_cause_event_sequence
FROM knowledge_contradictions
WHERE workspace_id = sqlc.arg(workspace_id) AND id = sqlc.arg(id);

-- name: GetKnowledgeContradictionByPair :one
SELECT id, workspace_id, project_id, left_revision_id, right_revision_id,
       status, state_revision, report_note, reported_at, reported_by,
       reported_by_type, detected_event_sequence,
       confirmed_at, confirmed_by, confirmed_by_type, confirm_note, confirm_event_sequence,
       dismissed_at, dismissed_by, dismissed_by_type, dismiss_note, dismiss_event_sequence,
       resolution_reason, resolved_at, resolved_by, resolved_by_type, resolution_note,
       resolution_event_sequence, resolution_cause_event_sequence
FROM knowledge_contradictions
WHERE workspace_id = sqlc.arg(workspace_id)
  AND left_revision_id = sqlc.arg(left_revision_id)
  AND right_revision_id = sqlc.arg(right_revision_id);

-- name: ListKnowledgeContradictionIDs :many
SELECT id
FROM knowledge_contradictions
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.arg(project_id) = '' OR project_id = sqlc.arg(project_id))
  AND ((sqlc.arg(status) = '' AND status IN ('proposed', 'open')) OR status = sqlc.arg(status))
  AND (sqlc.arg(revision_id) = '' OR left_revision_id = sqlc.arg(revision_id)
       OR right_revision_id = sqlc.arg(revision_id))
ORDER BY crewfold_timestamp_key(reported_at) DESC, id DESC
LIMIT sqlc.arg(result_limit);

-- name: ListOpenKnowledgeContradictionsForRevision :many
SELECT id, workspace_id, project_id, left_revision_id, right_revision_id,
       status, state_revision, report_note, reported_at, reported_by,
       reported_by_type, detected_event_sequence,
       confirmed_at, confirmed_by, confirmed_by_type, confirm_note, confirm_event_sequence,
       dismissed_at, dismissed_by, dismissed_by_type, dismiss_note, dismiss_event_sequence,
       resolution_reason, resolved_at, resolved_by, resolved_by_type, resolution_note,
       resolution_event_sequence, resolution_cause_event_sequence
FROM knowledge_contradictions
WHERE workspace_id = sqlc.arg(workspace_id) AND status = 'open'
  AND (left_revision_id = sqlc.arg(revision_id) OR right_revision_id = sqlc.arg(revision_id))
ORDER BY id
LIMIT 200;

-- name: CountOpenKnowledgeContradictionsForRevision :one
SELECT CAST(COUNT(*) AS INTEGER)
FROM knowledge_contradictions
WHERE workspace_id = sqlc.arg(workspace_id) AND status = 'open'
  AND (left_revision_id = sqlc.arg(revision_id) OR right_revision_id = sqlc.arg(revision_id));

-- name: ListAllOpenKnowledgeContradictionsForRevision :many
SELECT id, workspace_id, project_id, left_revision_id, right_revision_id,
       status, state_revision, report_note, reported_at, reported_by,
       reported_by_type, detected_event_sequence,
       confirmed_at, confirmed_by, confirmed_by_type, confirm_note, confirm_event_sequence,
       dismissed_at, dismissed_by, dismissed_by_type, dismiss_note, dismiss_event_sequence,
       resolution_reason, resolved_at, resolved_by, resolved_by_type, resolution_note,
       resolution_event_sequence, resolution_cause_event_sequence
FROM knowledge_contradictions
WHERE workspace_id = sqlc.arg(workspace_id) AND status = 'open'
  AND (left_revision_id = sqlc.arg(revision_id) OR right_revision_id = sqlc.arg(revision_id))
ORDER BY id;

-- name: ConfirmKnowledgeContradiction :execrows
UPDATE knowledge_contradictions
SET status = 'open', state_revision = state_revision + 1,
    confirmed_at = sqlc.arg(confirmed_at), confirmed_by = sqlc.arg(confirmed_by),
    confirmed_by_type = sqlc.arg(confirmed_by_type), confirm_note = sqlc.narg(confirm_note),
    confirm_event_sequence = sqlc.arg(confirm_event_sequence)
WHERE id = sqlc.arg(id) AND state_revision = sqlc.arg(expected_state_revision)
  AND status = 'proposed';

-- name: DismissKnowledgeContradiction :execrows
UPDATE knowledge_contradictions
SET status = 'dismissed', state_revision = state_revision + 1,
    dismissed_at = sqlc.arg(dismissed_at), dismissed_by = sqlc.arg(dismissed_by),
    dismissed_by_type = sqlc.arg(dismissed_by_type), dismiss_note = sqlc.narg(dismiss_note),
    dismiss_event_sequence = sqlc.arg(dismiss_event_sequence)
WHERE id = sqlc.arg(id) AND state_revision = sqlc.arg(expected_state_revision)
  AND status IN ('proposed', 'open');

-- name: ResolveKnowledgeContradiction :execrows
UPDATE knowledge_contradictions
SET status = 'resolved', state_revision = state_revision + 1,
    resolution_reason = sqlc.arg(resolution_reason), resolved_at = sqlc.arg(resolved_at),
    resolved_by = sqlc.arg(resolved_by), resolved_by_type = sqlc.arg(resolved_by_type),
    resolution_note = sqlc.arg(resolution_note),
    resolution_event_sequence = sqlc.arg(resolution_event_sequence),
    resolution_cause_event_sequence = sqlc.arg(resolution_cause_event_sequence)
WHERE id = sqlc.arg(id) AND state_revision = sqlc.arg(expected_state_revision)
  AND status = 'open';

-- name: InsertKnowledgeContradictionAuthorityCheck :exec
INSERT INTO knowledge_contradiction_authority_checks(
    id, workspace_id, contradiction_id, action, actor_id, actor_type,
    outcome, reason, note, idempotency_key, request_hash, event_sequence, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(contradiction_id),
    sqlc.arg(action), sqlc.arg(actor_id), sqlc.arg(actor_type),
    sqlc.arg(outcome), sqlc.arg(reason), sqlc.narg(note),
    sqlc.arg(idempotency_key), sqlc.arg(request_hash),
    sqlc.arg(event_sequence), sqlc.arg(created_at)
);

-- name: GetKnowledgeContradictionAuthorityCheckByKey :one
SELECT id, workspace_id, contradiction_id, action, actor_id, actor_type,
       outcome, reason, note, idempotency_key, request_hash, event_sequence, created_at
FROM knowledge_contradiction_authority_checks
WHERE actor_type = sqlc.arg(actor_type) AND actor_id = sqlc.arg(actor_id)
  AND action = sqlc.arg(action) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: ListKnowledgeContradictionAuthorityChecks :many
SELECT id, workspace_id, contradiction_id, action, actor_id, actor_type,
       outcome, reason, note, idempotency_key, request_hash, event_sequence, created_at
FROM knowledge_contradiction_authority_checks
WHERE workspace_id = sqlc.arg(workspace_id)
  AND contradiction_id = sqlc.arg(contradiction_id)
ORDER BY event_sequence DESC, id DESC
LIMIT 200;

-- name: CountKnowledgeContradictionAuthorityChecks :one
SELECT CAST(COUNT(*) AS INTEGER)
FROM knowledge_contradiction_authority_checks
WHERE workspace_id = sqlc.arg(workspace_id)
  AND contradiction_id = sqlc.arg(contradiction_id);

-- name: GetContradictionReporterRunScope :one
SELECT workspace_id, project_id, task_id, status
FROM runs
WHERE id = sqlc.arg(run_id);
