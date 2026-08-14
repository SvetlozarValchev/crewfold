-- name: CountOperatorWorkspaces :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM workspaces;

-- name: ListOperatorWorkspaces :many
SELECT id, name, revision, created_at, updated_at, created_by, updated_by
FROM workspaces
WHERE CAST(sqlc.arg(cursor_name) AS TEXT) = ''
   OR name > sqlc.arg(cursor_name)
   OR (name = sqlc.arg(cursor_name) AND id > sqlc.arg(cursor_id))
ORDER BY name, id
LIMIT sqlc.arg(result_limit);

-- name: CountOperatorProjects :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM projects
WHERE workspace_id = sqlc.arg(workspace_id);

-- name: ListOperatorProjects :many
SELECT id, workspace_id, name, revision, created_at, updated_at, created_by, updated_by
FROM projects
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (
    CAST(sqlc.arg(cursor_name) AS TEXT) = ''
    OR name > sqlc.arg(cursor_name)
    OR (name = sqlc.arg(cursor_name) AND id > sqlc.arg(cursor_id))
  )
ORDER BY name, id
LIMIT sqlc.arg(result_limit);

-- name: CountOperatorAgents :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM agents
WHERE workspace_id = sqlc.arg(workspace_id);

-- name: ListOperatorAgents :many
SELECT id, workspace_id, name, role, provider, runtime, enabled,
       max_concurrency, revision, created_at, updated_at, created_by, updated_by
FROM agents
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (
    CAST(sqlc.arg(cursor_name) AS TEXT) = ''
    OR name > sqlc.arg(cursor_name)
    OR (name = sqlc.arg(cursor_name) AND id > sqlc.arg(cursor_id))
  )
ORDER BY name, id
LIMIT sqlc.arg(result_limit);

-- name: CountOperatorObjectives :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM objectives
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR project_id = sqlc.arg(project_id));

-- name: ListOperatorObjectives :many
SELECT id, workspace_id, project_id, title, status, budget_tokens,
       budget_cost_cents, budget_time_seconds, revision, created_at,
       updated_at, created_by, updated_by
FROM objectives
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR project_id = sqlc.arg(project_id))
  AND (
    CAST(sqlc.arg(cursor_key) AS TEXT) = ''
    OR created_at > sqlc.arg(cursor_key)
    OR (created_at = sqlc.arg(cursor_key) AND id > sqlc.arg(cursor_id))
  )
ORDER BY created_at, id
LIMIT sqlc.arg(result_limit);

-- name: CountOperatorTasks :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM tasks t
WHERE t.workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR t.project_id = sqlc.arg(project_id))
  AND (
    CAST(sqlc.arg(ready_only) AS INTEGER) = 0
    OR (
      t.status = 'ready'
      AND NOT EXISTS (
        SELECT 1
        FROM task_dependencies dependency_link
        JOIN tasks dependency ON dependency.id = dependency_link.depends_on_task_id
        WHERE dependency_link.task_id = t.id AND dependency.status <> 'completed'
      )
    )
  );

-- name: ListOperatorTaskIDs :many
SELECT t.id
FROM tasks t
WHERE t.workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR t.project_id = sqlc.arg(project_id))
  AND (
    CAST(sqlc.arg(ready_only) AS INTEGER) = 0
    OR (
      t.status = 'ready'
      AND NOT EXISTS (
        SELECT 1
        FROM task_dependencies dependency_link
        JOIN tasks dependency ON dependency.id = dependency_link.depends_on_task_id
        WHERE dependency_link.task_id = t.id AND dependency.status <> 'completed'
      )
    )
  )
  AND (
    CAST(sqlc.arg(cursor_key) AS TEXT) = ''
    OR t.created_at > sqlc.arg(cursor_key)
    OR (t.created_at = sqlc.arg(cursor_key) AND t.id > sqlc.arg(cursor_id))
  )
ORDER BY t.created_at, t.id
LIMIT sqlc.arg(result_limit);

-- name: CountOperatorRuns :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM runs
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(task_id) AS TEXT) = '' OR task_id = sqlc.arg(task_id))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR status = sqlc.arg(status));

-- name: ListOperatorRuns :many
SELECT id, workspace_id, project_id, task_id, agent_id, runtime, provider,
       status, COALESCE(blocked_question, '') AS blocked_question,
       COALESCE(result_summary, '') AS result_summary,
       COALESCE(failure_code, '') AS failure_code, revision, created_at,
       updated_at, COALESCE(started_at, '') AS started_at,
       COALESCE(finished_at, '') AS finished_at,
       COALESCE(runtime_handle, '') AS runtime_handle
FROM runs
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(task_id) AS TEXT) = '' OR task_id = sqlc.arg(task_id))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR status = sqlc.arg(status))
  AND (
    CAST(sqlc.arg(cursor_key) AS TEXT) = ''
    OR created_at < sqlc.arg(cursor_key)
    OR (created_at = sqlc.arg(cursor_key) AND id < sqlc.arg(cursor_id))
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(result_limit);

-- name: CountOperatorClaims :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM work_claims
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR status = sqlc.arg(status));

-- name: ListOperatorClaimIDs :many
SELECT id
FROM work_claims
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR status = sqlc.arg(status))
  AND (
    CAST(sqlc.arg(cursor_key) AS TEXT) = ''
    OR created_at > sqlc.arg(cursor_key)
    OR (created_at = sqlc.arg(cursor_key) AND id > sqlc.arg(cursor_id))
  )
ORDER BY created_at, id
LIMIT sqlc.arg(result_limit);

-- name: CountOperatorOverlaps :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM work_overlaps
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR status = sqlc.arg(status));

-- name: ListOperatorOverlapIDs :many
SELECT id
FROM work_overlaps
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR status = sqlc.arg(status))
  AND (
    CAST(sqlc.arg(cursor_key) AS TEXT) = ''
    OR detected_at < sqlc.arg(cursor_key)
    OR (detected_at = sqlc.arg(cursor_key) AND id < sqlc.arg(cursor_id))
  )
ORDER BY detected_at DESC, id DESC
LIMIT sqlc.arg(result_limit);

-- name: CountOperatorClaimDrifts :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM claim_drifts
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR status = sqlc.arg(status));

-- name: ListOperatorClaimDriftIDs :many
SELECT id
FROM claim_drifts
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR status = sqlc.arg(status))
  AND (
    CAST(sqlc.arg(cursor_key) AS TEXT) = ''
    OR first_observed_at < sqlc.arg(cursor_key)
    OR (first_observed_at = sqlc.arg(cursor_key) AND id < sqlc.arg(cursor_id))
  )
ORDER BY first_observed_at DESC, id DESC
LIMIT sqlc.arg(result_limit);

-- name: CountOperatorMeetings :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM meetings
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR status = sqlc.arg(status));

-- name: ListOperatorMeetings :many
SELECT id, workspace_id, project_id, overlap_id, agenda, facilitator_agent_id,
       policy, reviewer_agent_id, allowed_actions_json, status, frozen_input_hash,
       deadline_at, stalled_reason, revision, created_at, updated_at, created_by,
       updated_by
FROM meetings
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR status = sqlc.arg(status))
  AND (
    CAST(sqlc.arg(cursor_key) AS TEXT) = ''
    OR created_at < sqlc.arg(cursor_key)
    OR (created_at = sqlc.arg(cursor_key) AND id < sqlc.arg(cursor_id))
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(result_limit);

-- name: CountOperatorEvents :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM events
WHERE workspace_id = sqlc.arg(workspace_id)
  AND sequence > sqlc.arg(after_sequence)
  AND sequence <= sqlc.arg(high_water);

-- name: FindFirstUnsupportedOperatorEvent :one
SELECT sequence, type
FROM events
WHERE workspace_id = sqlc.arg(workspace_id)
  AND sequence > sqlc.arg(after_sequence)
  AND sequence <= sqlc.arg(high_water)
  AND crewfold_event_type_known(type) = 0
ORDER BY sequence
LIMIT 1;

-- name: ListOperatorEvents :many
SELECT event_id, sequence, type, schema_version, occurred_at, recorded_at,
       actor_id, actor_type, workspace_id, entity_type, entity_id,
       entity_revision, correlation_id, causation_id, data_json
FROM events
WHERE workspace_id = sqlc.arg(workspace_id)
  AND sequence > sqlc.arg(page_after)
  AND sequence <= sqlc.arg(high_water)
ORDER BY sequence
LIMIT sqlc.arg(result_limit);

-- name: CountOperatorEntityEvents :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM events
WHERE workspace_id = sqlc.arg(workspace_id)
  AND entity_type = sqlc.arg(entity_type)
  AND entity_id = sqlc.arg(entity_id)
  AND sequence <= sqlc.arg(high_water);

-- name: FindFirstUnsupportedOperatorEntityEvent :one
SELECT sequence, type
FROM events
WHERE workspace_id = sqlc.arg(workspace_id)
  AND entity_type = sqlc.arg(entity_type)
  AND entity_id = sqlc.arg(entity_id)
  AND sequence <= sqlc.arg(high_water)
  AND crewfold_event_type_known(type) = 0
ORDER BY sequence DESC
LIMIT 1;

-- name: ListOperatorEntityEvents :many
SELECT event_id, sequence, type, schema_version, occurred_at, recorded_at,
       actor_id, actor_type, workspace_id, entity_type, entity_id,
       entity_revision, correlation_id, causation_id, data_json
FROM events
WHERE workspace_id = sqlc.arg(workspace_id)
  AND entity_type = sqlc.arg(entity_type)
  AND entity_id = sqlc.arg(entity_id)
  AND sequence <= sqlc.arg(high_water)
  AND (CAST(sqlc.arg(cursor_sequence) AS INTEGER) = 0 OR sequence < sqlc.arg(cursor_sequence))
ORDER BY sequence DESC
LIMIT sqlc.arg(result_limit);

-- name: CountOperatorApprovals :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM approval_requests approval
WHERE approval.workspace_id = sqlc.arg(workspace_id)
  AND EXISTS (
    SELECT 1
    FROM supervisor_actions action
    JOIN supervisor_action_receipts receipt ON receipt.action_id = action.id
    WHERE action.id = approval.action_id AND receipt.condition_key = action.condition_key
  )
  AND (
    CAST(sqlc.arg(project_id) AS TEXT) = ''
    OR EXISTS (SELECT 1 FROM supervisor_actions scoped_action WHERE scoped_action.id = approval.action_id AND scoped_action.project_id = sqlc.arg(project_id))
  )
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR approval.status = sqlc.arg(status))
  AND (CAST(sqlc.arg(action_id) AS TEXT) = '' OR approval.action_id = sqlc.arg(action_id));

-- name: ListOperatorApprovalIDs :many
SELECT approval.id
FROM approval_requests approval
WHERE approval.workspace_id = sqlc.arg(workspace_id)
  AND EXISTS (
    SELECT 1
    FROM supervisor_actions action
    JOIN supervisor_action_receipts receipt ON receipt.action_id = action.id
    WHERE action.id = approval.action_id AND receipt.condition_key = action.condition_key
  )
  AND (
    CAST(sqlc.arg(project_id) AS TEXT) = ''
    OR EXISTS (SELECT 1 FROM supervisor_actions scoped_action WHERE scoped_action.id = approval.action_id AND scoped_action.project_id = sqlc.arg(project_id))
  )
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR approval.status = sqlc.arg(status))
  AND (CAST(sqlc.arg(action_id) AS TEXT) = '' OR approval.action_id = sqlc.arg(action_id))
  AND (
    CAST(sqlc.arg(cursor_key) AS TEXT) = ''
    OR approval.created_at > sqlc.arg(cursor_key)
    OR (approval.created_at = sqlc.arg(cursor_key) AND approval.id > sqlc.arg(cursor_id))
  )
ORDER BY approval.created_at, approval.id
LIMIT sqlc.arg(result_limit);

-- name: CountOperatorCheckRuns :one
SELECT CAST(COUNT(*) AS INTEGER) AS total
FROM check_runs run
LEFT JOIN check_results result ON result.check_run_id = run.id
WHERE run.workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR run.project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(task_id) AS TEXT) = '' OR run.task_id = sqlc.arg(task_id))
  AND (CAST(sqlc.arg(requirement_id) AS TEXT) = '' OR run.requirement_id = sqlc.arg(requirement_id))
  AND (CAST(sqlc.arg(definition_id) AS TEXT) = '' OR run.definition_id = sqlc.arg(definition_id))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR run.status = sqlc.arg(status))
  AND (CAST(sqlc.arg(outcome) AS TEXT) = '' OR result.outcome = sqlc.arg(outcome));

-- name: ListOperatorCheckRunIDs :many
SELECT run.id
FROM check_runs run
LEFT JOIN check_results result ON result.check_run_id = run.id
WHERE run.workspace_id = sqlc.arg(workspace_id)
  AND (CAST(sqlc.arg(project_id) AS TEXT) = '' OR run.project_id = sqlc.arg(project_id))
  AND (CAST(sqlc.arg(task_id) AS TEXT) = '' OR run.task_id = sqlc.arg(task_id))
  AND (CAST(sqlc.arg(requirement_id) AS TEXT) = '' OR run.requirement_id = sqlc.arg(requirement_id))
  AND (CAST(sqlc.arg(definition_id) AS TEXT) = '' OR run.definition_id = sqlc.arg(definition_id))
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR run.status = sqlc.arg(status))
  AND (CAST(sqlc.arg(outcome) AS TEXT) = '' OR result.outcome = sqlc.arg(outcome))
  AND (
    CAST(sqlc.arg(cursor_key) AS TEXT) = ''
    OR run.created_at < sqlc.arg(cursor_key)
    OR (run.created_at = sqlc.arg(cursor_key) AND run.id < sqlc.arg(cursor_id))
  )
ORDER BY run.created_at DESC, run.id DESC
LIMIT sqlc.arg(result_limit);
