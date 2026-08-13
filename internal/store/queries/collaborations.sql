-- name: InsertParticipantThread :exec
INSERT INTO message_threads(
    id, workspace_id, project_id, task_id, subject, status, revision,
    created_at, updated_at, created_by, updated_by, kind, participant_revision,
    initial_participant_count
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), NULL, NULL, sqlc.arg(subject), 'open', 1,
    sqlc.arg(created_at), sqlc.arg(created_at), sqlc.arg(created_by), sqlc.arg(created_by),
    'participant_bound', 1, sqlc.arg(initial_participant_count)
);

-- name: GetEligibleParticipantAssignment :one
SELECT ta.id AS assignment_id, ta.revision AS assignment_revision, ta.lease_expires_at
FROM task_assignments ta
WHERE ta.task_id = sqlc.arg(task_id)
  AND ta.agent_id = sqlc.arg(agent_id)
  AND ta.status = 'active'
  AND crewfold_timestamp_key(ta.lease_expires_at) > crewfold_timestamp_key(CAST(sqlc.arg(observed_at) AS TEXT))
LIMIT 1;

-- name: InsertThreadParticipant :exec
INSERT INTO thread_participants(
    id, thread_id, workspace_id, agent_id, agent_name, task_id, task_title,
    project_id, project_name, assignment_id, assignment_revision,
    agent_revision, task_revision, ordinal, status, invited_at, invited_by
) VALUES (
    sqlc.arg(id), sqlc.arg(thread_id), sqlc.arg(workspace_id), sqlc.arg(agent_id), sqlc.arg(agent_name),
    sqlc.arg(task_id), sqlc.arg(task_title), sqlc.arg(project_id), sqlc.arg(project_name), sqlc.arg(assignment_id), sqlc.arg(assignment_revision),
    sqlc.arg(agent_revision), sqlc.arg(task_revision), sqlc.arg(ordinal),
    'active', sqlc.arg(invited_at), sqlc.arg(invited_by)
);

-- name: GetParticipantThreadState :one
SELECT id, workspace_id, COALESCE(project_id, '') AS project_id,
       COALESCE(task_id, '') AS task_id, subject, status, revision,
       created_at, updated_at, created_by, updated_by, kind, participant_revision,
       initial_participant_count
FROM message_threads
WHERE id = sqlc.arg(id) AND workspace_id = sqlc.arg(workspace_id)
  AND kind = 'participant_bound';

-- name: ListThreadParticipants :many
SELECT id, thread_id, workspace_id, agent_id, agent_name, task_id, task_title,
       project_id, project_name, assignment_id, assignment_revision,
       agent_revision, task_revision, ordinal, status, invited_at, invited_by
FROM thread_participants
WHERE thread_id = ?
ORDER BY ordinal;

-- name: FindThreadParticipantByBinding :one
SELECT id, thread_id, workspace_id, agent_id, agent_name, task_id, task_title,
       project_id, project_name, assignment_id, assignment_revision,
       agent_revision, task_revision, ordinal, status, invited_at, invited_by
FROM thread_participants
WHERE thread_id = sqlc.arg(thread_id)
  AND agent_id = sqlc.arg(agent_id)
  AND task_id = sqlc.arg(task_id);

-- name: ListThreadParticipantsByAgent :many
SELECT id, thread_id, workspace_id, agent_id, agent_name, task_id, task_title,
       project_id, project_name, assignment_id, assignment_revision,
       agent_revision, task_revision, ordinal, status, invited_at, invited_by
FROM thread_participants
WHERE thread_id = sqlc.arg(thread_id)
  AND agent_id = sqlc.arg(agent_id)
  AND status = 'active'
ORDER BY ordinal;

-- name: RunAuthorizedForMessage :one
SELECT CAST(EXISTS (
    SELECT 1
    FROM messages m
    JOIN message_threads th ON th.id = m.thread_id
    JOIN message_recipients mr ON mr.message_id = m.id
    JOIN runs r ON r.id = sqlc.arg(run_id)
    LEFT JOIN thread_participants p ON p.id = mr.recipient_participant_id
    WHERE m.id = sqlc.arg(message_id)
      AND mr.recipient_agent_id = r.agent_id
      AND r.workspace_id = m.workspace_id
      AND (
        (th.kind = 'direct' AND (m.project_id IS NULL OR m.project_id = r.project_id))
        OR
        (th.kind = 'participant_bound'
          AND p.thread_id = th.id
          AND p.status = 'active'
          AND p.agent_id = r.agent_id
          AND p.project_id = r.project_id
          AND p.task_id = r.task_id
          AND (SELECT COUNT(*) FROM thread_participants roster WHERE roster.thread_id = th.id) BETWEEN 2 AND 8
          AND (SELECT COUNT(*) FROM thread_participants roster WHERE roster.thread_id = th.id) >= th.initial_participant_count
          AND (SELECT COUNT(*) FROM thread_participants roster WHERE roster.thread_id = th.id) = th.initial_participant_count + th.participant_revision - 1
          AND (SELECT COUNT(DISTINCT roster.project_id) FROM thread_participants roster WHERE roster.thread_id = th.id) >= 2)
      )
) AS INTEGER) AS authorized;
