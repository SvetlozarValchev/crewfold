-- name: MeetingDependencyExists :one
SELECT EXISTS(
    SELECT 1 FROM task_dependencies
    WHERE task_id = sqlc.arg(task_id) AND depends_on_task_id = sqlc.arg(depends_on_task_id)
);

-- name: InsertMeetingDependency :exec
INSERT INTO task_dependencies(task_id, depends_on_task_id, created_at, created_by)
VALUES (sqlc.arg(task_id), sqlc.arg(depends_on_task_id), sqlc.arg(created_at), sqlc.arg(created_by));

-- name: TouchMeetingTask :exec
UPDATE tasks
SET revision = sqlc.arg(revision), updated_at = sqlc.arg(updated_at), updated_by = sqlc.arg(updated_by)
WHERE id = sqlc.arg(id);

-- name: ReleaseMeetingClaim :exec
UPDATE work_claims
SET status = sqlc.arg(status), revision = sqlc.arg(revision),
    updated_at = sqlc.arg(updated_at), updated_by = sqlc.arg(updated_by)
WHERE id = sqlc.arg(id);

-- name: InsertSplitTask :exec
INSERT INTO tasks(
    id, workspace_id, project_id, objective_id, title, description, status, blocked_reason,
    priority, budget_tokens, budget_cost_cents, budget_time_seconds, revision,
    created_at, updated_at, created_by, updated_by
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(project_id), sqlc.narg(objective_id),
    sqlc.arg(title), sqlc.narg(description), sqlc.arg(status), NULL,
    sqlc.arg(priority), sqlc.arg(budget_tokens), sqlc.arg(budget_cost_cents),
    sqlc.arg(budget_time_seconds), 1, sqlc.arg(created_at), sqlc.arg(updated_at),
    sqlc.arg(created_by), sqlc.arg(updated_by)
);

-- name: HasLiveTaskRun :one
SELECT EXISTS(
    SELECT 1 FROM runs
    WHERE task_id = ? AND status NOT IN ('stopped', 'lost', 'completed', 'start_failed', 'failed')
);

-- name: ReleaseTaskAssignmentsForMeeting :exec
UPDATE task_assignments
SET status = 'released', revision = revision + 1,
    updated_at = sqlc.arg(updated_at), updated_by = sqlc.arg(updated_by)
WHERE task_id = sqlc.arg(task_id) AND status = 'active';

-- name: InsertMeetingAssignment :exec
INSERT INTO task_assignments(
    id, task_id, agent_id, status, lease_expires_at, revision,
    created_at, updated_at, created_by, updated_by
) VALUES (
    sqlc.arg(id), sqlc.arg(task_id), sqlc.arg(agent_id), 'active',
    sqlc.arg(lease_expires_at), 1, sqlc.arg(created_at), sqlc.arg(updated_at),
    sqlc.arg(created_by), sqlc.arg(updated_by)
);

-- name: SetMeetingTaskAssigned :exec
UPDATE tasks
SET status = 'assigned', revision = sqlc.arg(revision),
    updated_at = sqlc.arg(updated_at), updated_by = sqlc.arg(updated_by)
WHERE id = sqlc.arg(id);

-- name: InsertMeetingTaskRole :exec
INSERT INTO task_roles(task_id, agent_id, role, source_meeting_id, created_at, created_by)
VALUES (
    sqlc.arg(task_id), sqlc.arg(agent_id), sqlc.arg(role),
    sqlc.arg(source_meeting_id), sqlc.arg(created_at), sqlc.arg(created_by)
);

-- name: SetMeetingTaskCancelled :exec
UPDATE tasks
SET status = 'cancelled', blocked_reason = NULL, revision = sqlc.arg(revision),
    updated_at = sqlc.arg(updated_at), updated_by = sqlc.arg(updated_by)
WHERE id = sqlc.arg(id);
