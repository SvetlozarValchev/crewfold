-- name: InsertKnowledgeItem :exec
INSERT INTO knowledge_items(
    id, workspace_id, project_id, task_scope_id, type,
    created_at, created_by, created_by_type
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(project_id), sqlc.narg(task_scope_id),
    sqlc.arg(type), sqlc.arg(created_at), sqlc.arg(created_by), sqlc.arg(created_by_type)
);

-- name: InsertKnowledgeRevision :exec
INSERT INTO knowledge_revisions(
    id, item_id, revision_number, state_revision, title, body, content_hash,
    review_status, currency_status, confidence, verification_status,
    freshness_policy, fresh_until, supersedes_revision_id,
    proposed_at, proposed_by, proposed_by_type
) VALUES (
    sqlc.arg(id), sqlc.arg(item_id), sqlc.arg(revision_number), 1,
    sqlc.arg(title), sqlc.arg(body), sqlc.arg(content_hash), 'proposed', 'pending',
    sqlc.arg(confidence), sqlc.arg(verification_status), sqlc.arg(freshness_policy),
    sqlc.narg(fresh_until), sqlc.narg(supersedes_revision_id),
    sqlc.arg(proposed_at), sqlc.arg(proposed_by), sqlc.arg(proposed_by_type)
);

-- name: InsertKnowledgeSource :exec
INSERT INTO knowledge_sources(
    revision_id, ordinal, source_type, source_id, source_revision, role
) VALUES (?, ?, ?, ?, ?, ?);

-- name: GetKnowledgeRevision :one
SELECT kr.id, kr.item_id, ki.workspace_id, ki.project_id,
       COALESCE(binding.task_id, ki.task_scope_id, '') AS task_scope_id, ki.type,
       kr.revision_number, kr.state_revision, kr.title, kr.body, kr.content_hash,
       kr.review_status, kr.currency_status, kr.confidence, kr.verification_status,
       kr.freshness_policy, kr.fresh_until, kr.supersedes_revision_id,
       kr.proposed_at, kr.proposed_by, kr.proposed_by_type,
       kr.accepted_at, kr.accepted_by, kr.accepted_by_type,
       kr.rejected_at, kr.rejected_by, kr.rejected_by_type,
       kr.stale_at, kr.stale_by, kr.stale_by_type,
       kr.decision_note, kr.stale_reason
FROM knowledge_revisions kr
JOIN knowledge_items ki ON ki.id = kr.item_id
LEFT JOIN knowledge_item_task_scopes binding ON binding.item_id = ki.id
WHERE kr.id = sqlc.arg(revision_id) AND ki.workspace_id = sqlc.arg(workspace_id);

-- name: ListKnowledgeRevisionIDs :many
SELECT kr.id
FROM knowledge_revisions kr
JOIN knowledge_items ki ON ki.id = kr.item_id
LEFT JOIN knowledge_item_task_scopes binding ON binding.item_id = ki.id
WHERE ki.workspace_id = sqlc.arg(workspace_id)
  AND ki.project_id = sqlc.arg(project_id)
  AND (sqlc.arg(task_scope_id) = '' OR COALESCE(binding.task_id, ki.task_scope_id) = sqlc.arg(task_scope_id))
  AND (sqlc.arg(type) = '' OR ki.type = sqlc.arg(type))
  AND (sqlc.arg(review_status) = '' OR kr.review_status = sqlc.arg(review_status))
  AND (sqlc.arg(currency_status) = '' OR kr.currency_status = sqlc.arg(currency_status))
ORDER BY kr.proposed_at DESC, kr.id DESC
LIMIT 200;

-- name: ListKnowledgeRevisionSources :many
SELECT revision_id, ordinal, source_type, source_id, source_revision, role
FROM knowledge_sources
WHERE revision_id = ?
ORDER BY ordinal;

-- name: MaxKnowledgeRevisionNumber :one
SELECT CAST(COALESCE(MAX(revision_number), 0) AS INTEGER) AS revision_number
FROM knowledge_revisions
WHERE item_id = ?;

-- name: FindLiveKnowledgeSuccessor :one
SELECT id
FROM knowledge_revisions
WHERE supersedes_revision_id = ? AND review_status IN ('proposed', 'accepted')
LIMIT 1;

-- name: FindAcceptedKnowledgeSuccessor :one
SELECT successor.id
FROM knowledge_revisions successor
JOIN knowledge_items ki ON ki.id = successor.item_id
WHERE successor.supersedes_revision_id = sqlc.arg(revision_id)
  AND successor.review_status = 'accepted'
  AND ki.workspace_id = sqlc.arg(workspace_id)
LIMIT 1;

-- name: AcceptKnowledgeRevision :execrows
UPDATE knowledge_revisions
SET review_status = 'accepted', currency_status = 'current',
    state_revision = state_revision + 1,
    accepted_at = sqlc.arg(accepted_at), accepted_by = sqlc.arg(accepted_by),
    accepted_by_type = sqlc.arg(accepted_by_type), decision_note = sqlc.narg(decision_note)
WHERE id = sqlc.arg(id) AND state_revision = sqlc.arg(expected_state_revision)
  AND review_status = 'proposed' AND currency_status = 'pending';

-- name: RejectKnowledgeRevision :execrows
UPDATE knowledge_revisions
SET review_status = 'rejected', currency_status = 'pending',
    state_revision = state_revision + 1,
    rejected_at = sqlc.arg(rejected_at), rejected_by = sqlc.arg(rejected_by),
    rejected_by_type = sqlc.arg(rejected_by_type), decision_note = sqlc.narg(decision_note)
WHERE id = sqlc.arg(id) AND state_revision = sqlc.arg(expected_state_revision)
  AND review_status = 'proposed' AND currency_status = 'pending';

-- name: MarkKnowledgeRevisionStale :execrows
UPDATE knowledge_revisions
SET currency_status = 'stale', state_revision = state_revision + 1,
    stale_at = sqlc.arg(stale_at), stale_by = sqlc.arg(stale_by),
    stale_by_type = sqlc.arg(stale_by_type), stale_reason = sqlc.arg(stale_reason)
WHERE id = sqlc.arg(id) AND state_revision = sqlc.arg(expected_state_revision)
  AND review_status = 'accepted' AND currency_status = 'current';

-- name: SupersedeKnowledgeRevision :execrows
UPDATE knowledge_revisions
SET currency_status = 'superseded', state_revision = state_revision + 1
WHERE id = sqlc.arg(id) AND state_revision = sqlc.arg(expected_state_revision)
  AND review_status = 'accepted' AND currency_status = 'current';

-- name: GetKnowledgeSourceTask :one
SELECT workspace_id, project_id, revision
FROM tasks
WHERE id = ?;

-- name: GetKnowledgeSourceMeeting :one
SELECT workspace_id, project_id, revision, status
FROM meetings
WHERE id = ?;

-- name: GetKnowledgeSourceMeetingProposal :one
SELECT m.workspace_id, m.project_id, mp.revision, mp.status AS proposal_status,
       m.status AS meeting_status
FROM meeting_proposals mp
JOIN meetings m ON m.id = mp.meeting_id
WHERE mp.id = ?;

-- name: GetKnowledgeSourceDomainAgent :one
SELECT agent.workspace_id, membership.project_id, membership.revision, membership.status, agent.enabled
FROM domain_agent_memberships membership
JOIN agents agent ON agent.id = membership.agent_id
WHERE membership.agent_id = ?;

-- name: GetKnowledgeActorRunWorkspace :one
SELECT workspace_id
FROM runs
WHERE id = ?;

-- name: GetKnowledgeActorDomainAgentWorkspace :one
SELECT agent.workspace_id
FROM domain_agent_memberships membership
JOIN agents agent ON agent.id = membership.agent_id
WHERE membership.agent_id = ? AND membership.status = 'active' AND agent.enabled = 1;

-- name: InsertKnowledgeAuthorityCheck :exec
INSERT INTO knowledge_authority_checks(
    id, workspace_id, revision_id, action, actor_id, actor_type,
    outcome, reason, note, idempotency_key, request_hash, event_sequence, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(revision_id), sqlc.arg(action),
    sqlc.arg(actor_id), sqlc.arg(actor_type), sqlc.arg(outcome), sqlc.arg(reason),
    sqlc.narg(note), sqlc.arg(idempotency_key), sqlc.arg(request_hash),
    sqlc.arg(event_sequence), sqlc.arg(created_at)
);

-- name: GetKnowledgeAuthorityCheckByKey :one
SELECT id, workspace_id, revision_id, action, actor_id, actor_type,
       outcome, reason, note, idempotency_key, request_hash, event_sequence, created_at
FROM knowledge_authority_checks
WHERE actor_type = sqlc.arg(actor_type) AND actor_id = sqlc.arg(actor_id)
  AND action = sqlc.arg(action) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: ListKnowledgeAuthorityChecks :many
SELECT id, workspace_id, revision_id, action, actor_id, actor_type,
       outcome, reason, note, idempotency_key, request_hash, event_sequence, created_at
FROM knowledge_authority_checks
WHERE workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.arg(revision_id) = '' OR revision_id = sqlc.arg(revision_id))
ORDER BY created_at, id;

-- name: InsertKnowledgeTaskScopeAnchor :exec
INSERT INTO knowledge_task_scope_anchors(task_id, workspace_id, project_id, created_at, created_by)
VALUES (sqlc.arg(task_id), sqlc.arg(workspace_id), sqlc.arg(project_id), sqlc.arg(created_at), sqlc.arg(created_by))
ON CONFLICT(task_id) DO NOTHING;

-- name: GetKnowledgeTaskScopeAnchor :one
SELECT task_id, workspace_id, project_id, created_at, created_by
FROM knowledge_task_scope_anchors
WHERE task_id = sqlc.arg(task_id);

-- name: InsertKnowledgeItemTaskScope :exec
INSERT INTO knowledge_item_task_scopes(item_id, task_id)
VALUES (sqlc.arg(item_id), sqlc.arg(task_id));
