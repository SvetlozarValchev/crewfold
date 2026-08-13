-- name: CountGrantedCheckCapacity :one
SELECT
 CAST(COUNT(*) AS INTEGER) AS unresolved_count,
 CAST(COALESCE(SUM(CASE WHEN status IN ('starting','running') THEN 1 ELSE 0 END),0) AS INTEGER) AS in_flight
FROM check_runs
WHERE source_type='agent_run' AND source_grant_id=sqlc.arg(grant_id)
 AND status IN ('requested','starting','running');

-- name: GetCheckGrantMaxInFlight :one
SELECT max_in_flight
FROM check_watch_grants
WHERE id=sqlc.arg(grant_id) AND revision=sqlc.arg(grant_revision);

-- name: GetLatestTaskRunCheckoutID :one
SELECT checkout_id
FROM runs
WHERE task_id=sqlc.arg(task_id) AND project_id=sqlc.arg(project_id)
ORDER BY CASE WHEN status IN ('requested','starting','active','blocked','stopping','lost') THEN 0 ELSE 1 END,
 created_at DESC,id DESC
LIMIT 1;

-- name: GetCheckRepository :one
SELECT * FROM repositories WHERE id=sqlc.arg(repository_id);

-- name: ResetExpiredCheckJobLeases :exec
UPDATE check_jobs
SET status='pending',lease_expires_at=NULL,updated_at=sqlc.arg(now)
WHERE status='leased'
 AND crewfold_timestamp_key(lease_expires_at)<=crewfold_timestamp_key(sqlc.arg(now));

-- name: GetNextPendingCheckRunID :one
SELECT job.check_run_id
FROM check_jobs job JOIN check_runs run ON run.id=job.check_run_id
WHERE job.status='pending' AND crewfold_timestamp_key(job.available_at)<=crewfold_timestamp_key(sqlc.arg(now))
 AND (run.source_type='owner' OR run.status<>'requested' OR
   (SELECT COUNT(*) FROM check_runs active
    WHERE active.source_grant_id=run.source_grant_id AND active.status IN ('starting','running')) < run.source_max_in_flight)
ORDER BY crewfold_timestamp_key(job.available_at),job.check_run_id
LIMIT 1;

-- name: LeaseCheckJob :execrows
UPDATE check_jobs
SET status='leased',lease_expires_at=sqlc.arg(lease_expires_at),attempts=attempts+1,updated_at=sqlc.arg(updated_at)
WHERE check_run_id=sqlc.arg(check_run_id) AND status='pending';

-- name: RecoverAllCheckJobLeases :exec
UPDATE check_jobs
SET status='pending',lease_expires_at=NULL,available_at=sqlc.arg(now),updated_at=sqlc.arg(now)
WHERE status='leased';

-- name: GetFrozenCheckRepository :one
SELECT *
FROM repositories
WHERE id=sqlc.arg(repository_id) AND object_format=sqlc.arg(object_format);

-- name: CheckLaunchReceiptAuthorityMatches :one
SELECT CAST(EXISTS(
 SELECT 1
 FROM check_runs run JOIN check_jobs job ON job.check_run_id=run.id
 WHERE run.id=sqlc.arg(check_run_id) AND run.status='requested'
  AND job.id=sqlc.arg(check_job_id) AND job.status='leased'
  AND run.definition_sha256=sqlc.arg(definition_sha256)
  AND run.repository_id=sqlc.arg(repository_id)
  AND run.repository_object_format=sqlc.arg(repository_object_format)
  AND run.checkout_id=sqlc.arg(checkout_id)
  AND run.source_type=sqlc.arg(source_type)
  AND run.source_actor_id=sqlc.arg(source_actor_id)
  AND run.source_agent_id IS sqlc.narg(source_agent_id)
  AND run.source_agent_revision IS sqlc.narg(source_agent_revision)
  AND run.source_run_id IS sqlc.narg(source_run_id)
  AND run.source_grant_id IS sqlc.narg(source_grant_id)
  AND run.source_grant_revision IS sqlc.narg(source_grant_revision)
) AS INTEGER) AS authority_matches;

-- name: CountGrantActiveChecks :one
SELECT CAST(COUNT(*) AS INTEGER)
FROM check_runs
WHERE source_grant_id=sqlc.arg(grant_id) AND status IN ('starting','running');

-- name: MarkCheckRunStarting :execrows
UPDATE check_runs
SET status='starting',revision=revision+1,updated_at=sqlc.arg(updated_at),updated_by='crewfold-check-worker'
WHERE id=sqlc.arg(check_run_id) AND status='requested';

-- name: InsertCheckLaunchReceipt :exec
INSERT INTO check_launch_receipts(
 id,check_run_id,check_job_id,operation_id,effective_spec_sha256,effective_working_directory,
 launchable,preflight_failure_code,preflight_failure_diagnostic,definition_sha256,
 source_type,source_actor_id,source_agent_id,source_agent_revision,source_run_id,source_grant_id,source_grant_revision,
 observation_available,repository_id,object_format,checkout_id,branch,head_commit,dirty,dirty_paths_json,
 observed_at,diagnostic_code,diagnostic,created_at,created_by
) VALUES(
 sqlc.arg(id),sqlc.arg(check_run_id),sqlc.arg(check_job_id),sqlc.arg(operation_id),sqlc.arg(effective_spec_sha256),sqlc.arg(effective_working_directory),
 sqlc.arg(launchable),NULLIF(sqlc.arg(preflight_failure_code),''),NULLIF(sqlc.arg(preflight_failure_diagnostic),''),sqlc.arg(definition_sha256),
 sqlc.arg(source_type),sqlc.arg(source_actor_id),NULLIF(sqlc.arg(source_agent_id),''),NULLIF(sqlc.arg(source_agent_revision),0),NULLIF(sqlc.arg(source_run_id),''),NULLIF(sqlc.arg(source_grant_id),''),NULLIF(sqlc.arg(source_grant_revision),0),
 sqlc.arg(observation_available),sqlc.arg(repository_id),sqlc.arg(object_format),sqlc.arg(checkout_id),NULLIF(sqlc.arg(branch),''),NULLIF(sqlc.arg(head_commit),''),sqlc.arg(dirty),sqlc.arg(dirty_paths_json),
 sqlc.arg(observed_at),NULLIF(sqlc.arg(diagnostic_code),''),NULLIF(sqlc.arg(diagnostic),''),sqlc.arg(created_at),'crewfold-check-worker'
);

-- name: GetCheckJobStatus :one
SELECT status FROM check_jobs WHERE check_run_id=sqlc.arg(check_run_id);

-- name: GetCheckLaunchReceiptLaunchable :one
SELECT launchable FROM check_launch_receipts WHERE check_run_id=sqlc.arg(check_run_id);

-- name: BindCheckRuntime :execrows
UPDATE check_runs
SET runtime_handle=sqlc.arg(runtime_handle),revision=revision+1,updated_at=sqlc.arg(updated_at),updated_by='crewfold-check-worker'
WHERE id=sqlc.arg(check_run_id) AND status='starting' AND runtime_handle IS NULL;

-- name: MarkCheckRunRunning :execrows
UPDATE check_runs
SET status='running',revision=revision+1,started_at=sqlc.arg(started_at),updated_at=sqlc.arg(started_at),updated_by='crewfold-check-worker'
WHERE id=sqlc.arg(check_run_id) AND status='starting' AND runtime_handle IS NOT NULL;

-- name: ReleaseCheckJobLease :execrows
UPDATE check_jobs
SET status='pending',lease_expires_at=NULL,available_at=sqlc.arg(available_at),updated_at=sqlc.arg(updated_at)
WHERE check_run_id=sqlc.arg(check_run_id) AND status='leased';

-- name: GetFrozenCheckCheckoutState :one
SELECT project_id,repository_id,path,write_mode,availability,revision
FROM checkouts
WHERE id=sqlc.arg(checkout_id);

-- name: ListCheckFreshnessHistory :many
SELECT *
FROM check_result_freshness
WHERE check_result_id=sqlc.arg(check_result_id)
ORDER BY revision;

-- name: ListCheckArtifactsForResult :many
SELECT *
FROM check_artifacts
WHERE check_result_id=sqlc.arg(check_result_id)
ORDER BY kind;

-- name: ListCheckEvidenceForRequirement :many
SELECT *
FROM check_requirement_evidence
WHERE requirement_id=sqlc.arg(requirement_id) AND requirement_revision=sqlc.arg(requirement_revision)
ORDER BY created_at,id;

-- name: ListGrantedCheckRunIDs :many
SELECT run.id
FROM check_runs run
JOIN check_watch_grant_definitions allowed ON allowed.definition_id=run.definition_id
 AND allowed.definition_content_revision=run.definition_content_revision
 AND allowed.definition_sha256=run.definition_sha256
WHERE allowed.grant_id=sqlc.arg(grant_id) AND run.project_id=sqlc.arg(project_id)
 AND run.status='finished'
 AND (CAST(sqlc.arg(after_run_id) AS TEXT)='' OR run.id<sqlc.arg(after_run_id))
ORDER BY run.id DESC
LIMIT sqlc.arg(result_limit);

-- name: GetLatestCheckRunForRequirement :one
SELECT id,status
FROM check_runs
WHERE requirement_id=sqlc.arg(requirement_id) AND requirement_revision=sqlc.arg(requirement_revision)
ORDER BY created_at DESC,id DESC
LIMIT 1;

-- name: GetCheckResultByRun :one
SELECT * FROM check_results WHERE check_run_id=sqlc.arg(check_run_id);

-- name: GetLatestCheckFreshnessByResult :one
SELECT *
FROM check_result_freshness
WHERE check_result_id=sqlc.arg(check_result_id)
ORDER BY revision DESC
LIMIT 1;
