-- name: FindActiveMeetingForOverlap :one
SELECT id
FROM meetings
WHERE overlap_id = ? AND status NOT IN ('concluded', 'cancelled')
LIMIT 1;

-- name: InsertMeeting :exec
INSERT INTO meetings(
    id, workspace_id, project_id, overlap_id, agenda, facilitator_agent_id, policy,
    reviewer_agent_id, allowed_actions_json, status, frozen_input_json, frozen_input_hash,
    deadline_at, stalled_reason, revision, created_at, updated_at, created_by, updated_by
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(project_id), sqlc.arg(overlap_id),
    sqlc.arg(agenda), sqlc.arg(facilitator_agent_id), sqlc.arg(policy), sqlc.narg(reviewer_agent_id),
    sqlc.arg(allowed_actions_json), sqlc.arg(status), sqlc.arg(frozen_input_json),
    sqlc.arg(frozen_input_hash), sqlc.arg(deadline_at), sqlc.narg(stalled_reason), sqlc.arg(revision),
    sqlc.arg(created_at), sqlc.arg(updated_at), sqlc.arg(created_by), sqlc.arg(updated_by)
);

-- name: GetMeeting :one
SELECT id, workspace_id, project_id, overlap_id, agenda, facilitator_agent_id, policy,
       reviewer_agent_id, allowed_actions_json, status, frozen_input_json,
       frozen_input_hash, deadline_at, stalled_reason, revision,
       created_at, updated_at, created_by, updated_by
FROM meetings
WHERE id = ? AND workspace_id = ?;

-- name: UpdateMeetingState :exec
UPDATE meetings
SET status = sqlc.arg(status), stalled_reason = sqlc.narg(stalled_reason),
    revision = sqlc.arg(revision), updated_at = sqlc.arg(updated_at), updated_by = sqlc.arg(updated_by)
WHERE id = sqlc.arg(id);

-- name: InsertMeetingParticipant :exec
INSERT INTO meeting_participants(meeting_id, agent_id, task_id, ordinal, status)
VALUES (sqlc.arg(meeting_id), sqlc.arg(agent_id), sqlc.narg(task_id), sqlc.arg(ordinal), sqlc.arg(status));

-- name: UpdateMeetingParticipantStatus :exec
UPDATE meeting_participants
SET status = ?
WHERE meeting_id = ? AND agent_id = ?;

-- name: ListMeetingParticipants :many
SELECT meeting_id, agent_id, task_id, ordinal, status
FROM meeting_participants
WHERE meeting_id = ?
ORDER BY ordinal;

-- name: GetMeetingContribution :one
SELECT id, meeting_id, agent_id, round, summary, evidence_json, submitted_at
FROM meeting_contributions
WHERE meeting_id = ? AND agent_id = ? AND round = ?;

-- name: InsertMeetingContribution :exec
INSERT INTO meeting_contributions(
    id, meeting_id, agent_id, round, summary, evidence_json, submitted_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListMeetingContributions :many
SELECT id, meeting_id, agent_id, round, summary, evidence_json, submitted_at
FROM meeting_contributions
WHERE meeting_id = ?
ORDER BY submitted_at, id;

-- name: InsertMeetingProposal :exec
INSERT INTO meeting_proposals(
    id, meeting_id, proposed_by, summary, status, revision, proposed_at
) VALUES (?, ?, ?, ?, ?, 1, ?);

-- name: GetMeetingProposal :one
SELECT id, meeting_id, proposed_by, summary, status, revision, proposed_at,
       decided_at, decision_note
FROM meeting_proposals
WHERE meeting_id = ?;

-- name: DecideMeetingProposal :exec
UPDATE meeting_proposals
SET status = sqlc.arg(status), revision = revision + 1,
    decided_at = sqlc.arg(decided_at), decision_note = sqlc.narg(decision_note)
WHERE id = sqlc.arg(id);

-- name: InsertMeetingAction :exec
INSERT INTO meeting_actions(
    id, proposal_id, ordinal, type, payload_json, status
) VALUES (?, ?, ?, ?, ?, ?);

-- name: ListMeetingActions :many
SELECT id, proposal_id, ordinal, type, payload_json, status,
       result_entity_id, diagnostic, applied_at
FROM meeting_actions
WHERE proposal_id = ?
ORDER BY ordinal;

-- name: MarkMeetingActionApplied :exec
UPDATE meeting_actions
SET status = sqlc.arg(status), result_entity_id = sqlc.narg(result_entity_id), applied_at = sqlc.arg(applied_at)
WHERE id = sqlc.arg(id);

-- name: MaxEventSequence :one
SELECT CAST(COALESCE(MAX(sequence), 0) AS INTEGER) AS sequence FROM events;
