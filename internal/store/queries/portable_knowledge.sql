-- name: ListPortableKnowledgeItems :many
SELECT ki.id, ki.workspace_id, ki.project_id,
       COALESCE(binding.task_id, ki.task_scope_id, '') AS task_scope_id,
       ki.type, ki.created_at, ki.created_by, ki.created_by_type
FROM knowledge_items ki
LEFT JOIN knowledge_item_task_scopes binding ON binding.item_id = ki.id
WHERE ki.workspace_id = sqlc.arg(workspace_id) AND ki.project_id = sqlc.arg(project_id)
ORDER BY ki.id;

-- name: PortableKnowledgeEventHighWater :one
SELECT CAST(COALESCE(MAX(sequence), 0) AS INTEGER) FROM events;

-- name: PortableKnowledgeSnapshotPreflight :one
SELECT
    (SELECT COUNT(*) FROM knowledge_items item
     WHERE item.workspace_id = sqlc.arg(workspace_id) AND item.project_id = sqlc.arg(project_id)) AS item_count,
    (SELECT COUNT(*) FROM knowledge_revisions revision
     JOIN knowledge_items item ON item.id = revision.item_id
     WHERE item.workspace_id = sqlc.arg(workspace_id) AND item.project_id = sqlc.arg(project_id)) AS revision_count,
    (SELECT COUNT(*) FROM knowledge_contradictions contradiction
     WHERE contradiction.workspace_id = sqlc.arg(workspace_id) AND contradiction.project_id = sqlc.arg(project_id)) AS contradiction_count,
    COALESCE((SELECT SUM(
        length(CAST(revision.title AS BLOB)) + length(CAST(revision.body AS BLOB)) +
        length(CAST(COALESCE(revision.decision_note, '') AS BLOB)) +
        length(CAST(COALESCE(revision.stale_reason, '') AS BLOB)) + 1
    ) FROM knowledge_revisions revision
      JOIN knowledge_items item ON item.id = revision.item_id
      WHERE item.workspace_id = sqlc.arg(workspace_id) AND item.project_id = sqlc.arg(project_id)), 0) +
    COALESCE((SELECT SUM(
        length(CAST(source.source_type AS BLOB)) + length(CAST(source.source_id AS BLOB)) +
        length(CAST(source.role AS BLOB)) + 1
    ) FROM knowledge_sources source
      JOIN knowledge_revisions revision ON revision.id = source.revision_id
      JOIN knowledge_items item ON item.id = revision.item_id
      WHERE item.workspace_id = sqlc.arg(workspace_id) AND item.project_id = sqlc.arg(project_id)), 0) +
    COALESCE((SELECT SUM(
        length(CAST(contradiction.report_note AS BLOB)) +
        length(CAST(COALESCE(contradiction.confirm_note, '') AS BLOB)) +
        length(CAST(COALESCE(contradiction.dismiss_note, '') AS BLOB)) +
        length(CAST(COALESCE(contradiction.resolution_note, '') AS BLOB)) + 1
    ) FROM knowledge_contradictions contradiction
      WHERE contradiction.workspace_id = sqlc.arg(workspace_id) AND contradiction.project_id = sqlc.arg(project_id)), 0) +
    (SELECT COUNT(*) FROM knowledge_items item
     WHERE item.workspace_id = sqlc.arg(workspace_id) AND item.project_id = sqlc.arg(project_id)) AS payload_byte_floor;

-- name: ListPortableKnowledgeRevisionIDsForItem :many
SELECT id FROM knowledge_revisions WHERE item_id = ? ORDER BY revision_number, id;

-- name: ListPortableKnowledgeTaskScopeAnchors :many
SELECT DISTINCT a.task_id, a.workspace_id, a.project_id, a.created_at, a.created_by
FROM knowledge_task_scope_anchors a
JOIN knowledge_item_task_scopes binding ON binding.task_id = a.task_id
JOIN knowledge_items ki ON ki.id = binding.item_id
WHERE ki.workspace_id = sqlc.arg(workspace_id) AND ki.project_id = sqlc.arg(project_id)
ORDER BY a.task_id;

-- name: ListPortableKnowledgeContradictions :many
SELECT id, workspace_id, project_id, left_revision_id, right_revision_id,
       status, state_revision, report_note, reported_at, reported_by,
       reported_by_type, detected_event_sequence,
       confirmed_at, confirmed_by, confirmed_by_type, confirm_note, confirm_event_sequence,
       dismissed_at, dismissed_by, dismissed_by_type, dismiss_note, dismiss_event_sequence,
       resolution_reason, resolved_at, resolved_by, resolved_by_type, resolution_note,
       resolution_event_sequence, resolution_cause_event_sequence
FROM knowledge_contradictions
WHERE workspace_id = sqlc.arg(workspace_id) AND project_id = sqlc.arg(project_id)
ORDER BY id;

-- name: InsertPortableKnowledgeRevision :exec
INSERT INTO knowledge_revisions(
    id, item_id, revision_number, state_revision, title, body, content_hash,
    review_status, currency_status, confidence, verification_status,
    freshness_policy, fresh_until, supersedes_revision_id,
    proposed_at, proposed_by, proposed_by_type,
    accepted_at, accepted_by, accepted_by_type,
    rejected_at, rejected_by, rejected_by_type,
    stale_at, stale_by, stale_by_type, decision_note, stale_reason
) VALUES (
    sqlc.arg(id), sqlc.arg(item_id), sqlc.arg(revision_number), sqlc.arg(state_revision),
    sqlc.arg(title), sqlc.arg(body), sqlc.arg(content_hash),
    sqlc.arg(review_status), sqlc.arg(currency_status), sqlc.arg(confidence),
    sqlc.arg(verification_status), sqlc.arg(freshness_policy), sqlc.narg(fresh_until),
    sqlc.narg(supersedes_revision_id), sqlc.arg(proposed_at), sqlc.arg(proposed_by),
    sqlc.arg(proposed_by_type), sqlc.narg(accepted_at), sqlc.narg(accepted_by),
    sqlc.narg(accepted_by_type), sqlc.narg(rejected_at), sqlc.narg(rejected_by),
    sqlc.narg(rejected_by_type), sqlc.narg(stale_at), sqlc.narg(stale_by),
    sqlc.narg(stale_by_type), sqlc.narg(decision_note), sqlc.narg(stale_reason)
);

-- name: InsertPortableKnowledgeContradiction :exec
INSERT INTO knowledge_contradictions(
    id, workspace_id, project_id, left_revision_id, right_revision_id,
    status, state_revision, report_note, reported_at, reported_by,
    reported_by_type, detected_event_sequence,
    confirmed_at, confirmed_by, confirmed_by_type, confirm_note, confirm_event_sequence,
    dismissed_at, dismissed_by, dismissed_by_type, dismiss_note, dismiss_event_sequence,
    resolution_reason, resolved_at, resolved_by, resolved_by_type, resolution_note,
    resolution_event_sequence, resolution_cause_event_sequence
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(project_id),
    sqlc.arg(left_revision_id), sqlc.arg(right_revision_id), sqlc.arg(status),
    sqlc.arg(state_revision), sqlc.arg(report_note), sqlc.arg(reported_at),
    sqlc.arg(reported_by), sqlc.arg(reported_by_type), sqlc.arg(detected_event_sequence),
    sqlc.narg(confirmed_at), sqlc.narg(confirmed_by), sqlc.narg(confirmed_by_type),
    sqlc.narg(confirm_note), sqlc.narg(confirm_event_sequence),
    sqlc.narg(dismissed_at), sqlc.narg(dismissed_by), sqlc.narg(dismissed_by_type),
    sqlc.narg(dismiss_note), sqlc.narg(dismiss_event_sequence),
    sqlc.narg(resolution_reason), sqlc.narg(resolved_at), sqlc.narg(resolved_by),
    sqlc.narg(resolved_by_type), sqlc.narg(resolution_note),
    sqlc.narg(resolution_event_sequence), sqlc.narg(resolution_cause_event_sequence)
);

-- name: InsertPortableKnowledgeImportEntity :exec
INSERT INTO knowledge_import_entities(import_id, entity_type, entity_id, event_sequence, imported_at)
VALUES(sqlc.arg(import_id), sqlc.arg(entity_type), sqlc.arg(entity_id), sqlc.narg(event_sequence), sqlc.arg(imported_at));

-- name: InsertPortableKnowledgeImportReceipt :exec
INSERT INTO knowledge_imports(
    id, bundle_id, workspace_id, project_id, content_sha256, rendering_sha256,
    manifest_json, markdown, idempotency_key, request_hash, imported_at,
    imported_by, imported_by_type, created_workspace, created_project,
    created_task_scope_anchors, completed_event_sequence
) VALUES (
    sqlc.arg(id), sqlc.arg(bundle_id), sqlc.arg(workspace_id), sqlc.arg(project_id),
    sqlc.arg(content_sha256), sqlc.arg(rendering_sha256), sqlc.arg(manifest_json),
    sqlc.arg(markdown), sqlc.arg(idempotency_key), sqlc.arg(request_hash),
    sqlc.arg(imported_at), sqlc.arg(imported_by), sqlc.arg(imported_by_type),
    sqlc.arg(created_workspace), sqlc.arg(created_project),
    sqlc.arg(created_task_scope_anchors), sqlc.arg(completed_event_sequence)
);

-- name: GetPortableKnowledgeImportReceipt :one
SELECT id, bundle_id, workspace_id, project_id, content_sha256, rendering_sha256,
       manifest_json, markdown, idempotency_key, request_hash, imported_at,
       imported_by, imported_by_type, created_workspace, created_project,
       created_task_scope_anchors, completed_event_sequence
FROM knowledge_imports
WHERE workspace_id = sqlc.arg(workspace_id) AND project_id = sqlc.arg(project_id);

-- name: InsertPortableWorkspace :exec
INSERT INTO workspaces(id, name, revision, created_at, updated_at, created_by, updated_by)
VALUES(sqlc.arg(id), sqlc.arg(name), sqlc.arg(revision), sqlc.arg(created_at),
       sqlc.arg(updated_at), sqlc.arg(created_by), sqlc.arg(updated_by));

-- name: InsertPortableProject :exec
INSERT INTO projects(id, workspace_id, name, revision, created_at, updated_at, created_by, updated_by)
VALUES(sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(name), sqlc.arg(revision),
       sqlc.arg(created_at), sqlc.arg(updated_at), sqlc.arg(created_by), sqlc.arg(updated_by));

-- name: CountPortableKnowledgeProjectState :one
WITH target AS (
    SELECT CAST(sqlc.arg(target_workspace_id) AS TEXT) AS workspace_id,
           CAST(sqlc.arg(target_project_id) AS TEXT) AS project_id
)
SELECT
    (SELECT COUNT(*) FROM knowledge_items item, target
     WHERE item.workspace_id = target.workspace_id AND item.project_id = target.project_id) AS item_count,
    (SELECT COUNT(*) FROM knowledge_contradictions contradiction, target
     WHERE contradiction.workspace_id = target.workspace_id AND contradiction.project_id = target.project_id) AS contradiction_count,
    (SELECT COUNT(*) FROM knowledge_imports receipt, target
     WHERE receipt.workspace_id = target.workspace_id AND receipt.project_id = target.project_id) AS import_count;

-- name: CountPortableKnowledgeItemIdentity :one
SELECT COUNT(*) FROM knowledge_items WHERE id = ?;

-- name: CountPortableKnowledgeRevisionIdentity :one
SELECT COUNT(*) FROM knowledge_revisions WHERE id = ?;

-- name: CountPortableKnowledgeContradictionIdentity :one
SELECT COUNT(*) FROM knowledge_contradictions
WHERE id = sqlc.arg(id)
   OR (workspace_id = sqlc.arg(workspace_id)
       AND left_revision_id = sqlc.arg(left_revision_id)
       AND right_revision_id = sqlc.arg(right_revision_id));

-- name: ListPortableKnowledgeTargetAnchors :many
SELECT task_id, workspace_id, project_id, created_at, created_by
FROM knowledge_task_scope_anchors
WHERE workspace_id = sqlc.arg(workspace_id) AND project_id = sqlc.arg(project_id)
ORDER BY task_id;

-- name: GetPortableTaskIdentity :one
SELECT workspace_id, project_id, created_at, created_by FROM tasks WHERE id = ?;

-- name: GetPortableKnowledgeAnchorIdentity :one
SELECT workspace_id, project_id, created_at, created_by
FROM knowledge_task_scope_anchors WHERE task_id = ?;

-- name: InsertPortableKnowledgeAnchor :exec
INSERT INTO knowledge_task_scope_anchors(task_id, workspace_id, project_id, created_at, created_by)
VALUES(sqlc.arg(task_id), sqlc.arg(workspace_id), sqlc.arg(project_id), sqlc.arg(created_at), sqlc.arg(created_by));
