-- name: InsertImmutableArtifact :exec
INSERT INTO immutable_artifacts(content_sha256,byte_size,created_at,created_by)
VALUES(sqlc.arg(content_sha256),sqlc.arg(byte_size),sqlc.arg(created_at),sqlc.arg(created_by))
ON CONFLICT(content_sha256) DO NOTHING;

-- name: InsertRunLogArtifact :exec
INSERT INTO run_log_artifacts(
 id,run_id,kind,content_sha256,captured_bytes,omitted_bytes,truncated,created_at,created_by
) VALUES(
 sqlc.arg(id),sqlc.arg(run_id),sqlc.arg(kind),sqlc.arg(content_sha256),
 sqlc.arg(captured_bytes),sqlc.arg(omitted_bytes),sqlc.arg(truncated),
 sqlc.arg(created_at),'subsystem:run-worker'
);

-- name: ListRunLogArtifacts :many
SELECT id,run_id,kind,content_sha256,captured_bytes,omitted_bytes,truncated,created_at,created_by
FROM run_log_artifacts
WHERE run_id=sqlc.arg(run_id)
ORDER BY kind;

-- name: InsertRunLossResolution :exec
INSERT INTO run_loss_resolutions(
 run_id,lost_revision,resolution,note,event_sequence,resolved_at,resolved_by
) VALUES(
 sqlc.arg(run_id),sqlc.arg(lost_revision),'owner_confirmed_effects_ended',
 sqlc.arg(note),sqlc.arg(event_sequence),sqlc.arg(resolved_at),'local-owner'
);

-- name: GetRunLossResolution :one
SELECT run_id,lost_revision,resolution,note,event_sequence,resolved_at,resolved_by
FROM run_loss_resolutions
WHERE run_id=sqlc.arg(run_id);

-- name: ListReferencedImmutableArtifacts :many
SELECT referenced.content_sha256,referenced.byte_size,referenced.kind
FROM (
  SELECT artifact.content_sha256 AS content_sha256,
         artifact.byte_size AS byte_size,
         'check_artifact' AS kind
  FROM immutable_artifacts artifact
  WHERE EXISTS(
    SELECT 1 FROM check_artifacts checked
    WHERE checked.content_sha256=artifact.content_sha256
  )
  UNION ALL
  SELECT artifact.content_sha256 AS content_sha256,
         artifact.byte_size AS byte_size,
         'run_log_artifact' AS kind
  FROM immutable_artifacts artifact
  WHERE EXISTS(
    SELECT 1 FROM run_log_artifacts run_log
    WHERE run_log.content_sha256=artifact.content_sha256
  )
) referenced
ORDER BY referenced.kind,referenced.content_sha256;
