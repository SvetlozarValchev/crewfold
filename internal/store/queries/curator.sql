-- name: GetEffectiveCuratorRule :one
SELECT id, workspace_id, name, revision, enabled, created_at, created_by, event_sequence
FROM curator_rules
WHERE workspace_id = sqlc.arg(workspace_id) AND name = sqlc.arg(name)
ORDER BY revision DESC
LIMIT 1;

-- name: InsertCuratorRule :exec
INSERT INTO curator_rules(
    id, workspace_id, name, revision, enabled, created_at, created_by, event_sequence
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(name), sqlc.arg(revision),
    sqlc.arg(enabled), sqlc.arg(created_at), sqlc.arg(created_by),
    sqlc.arg(event_sequence)
);

-- name: ListCuratorMeetingProposalCandidates :many
SELECT mp.id AS proposal_id, mp.revision AS proposal_revision, mp.summary,
       mp.proposed_at, m.id AS meeting_id, m.agenda, m.workspace_id, m.project_id,
       mp.status AS proposal_status, m.status AS meeting_status
FROM meeting_proposals mp
JOIN meetings m ON m.id = mp.meeting_id
WHERE m.workspace_id = sqlc.arg(workspace_id)
  AND m.project_id = sqlc.arg(project_id)
  AND mp.status = 'accepted'
  AND m.status = 'concluded'
  AND NOT EXISTS (
      SELECT 1
      FROM curator_derivations d
      WHERE d.workspace_id = m.workspace_id
        AND d.rule_name = sqlc.arg(rule_name)
        AND d.source_type = 'meeting_proposal'
        AND d.source_id = mp.id
        AND d.source_revision = mp.revision
  )
ORDER BY
  CASE WHEN length(CAST(m.agenda AS BLOB)) BETWEEN 1 AND 160
             AND instr(m.agenda, char(0)) = 0
             AND crewfold_utf8_valid(m.agenda) = 1
             AND length(CAST(mp.summary AS BLOB)) BETWEEN 1 AND 2048
             AND instr(mp.summary, char(0)) = 0
             AND crewfold_utf8_valid(mp.summary) = 1
       THEN 0 ELSE 1 END,
  crewfold_timestamp_key(mp.proposed_at), mp.id
LIMIT sqlc.arg(candidate_limit);

-- name: GetCuratorMeetingProposalSource :one
SELECT mp.id AS proposal_id, mp.revision AS proposal_revision, mp.summary,
       mp.proposed_at, m.id AS meeting_id, m.agenda, m.workspace_id, m.project_id,
       mp.status AS proposal_status, m.status AS meeting_status
FROM meeting_proposals mp
JOIN meetings m ON m.id = mp.meeting_id
WHERE mp.id = sqlc.arg(proposal_id);

-- name: InsertCuratorDerivation :exec
INSERT INTO curator_derivations(
    id, workspace_id, project_id, rule_id, rule_name, rule_revision,
    source_type, source_id, source_revision, source_content_hash,
    knowledge_revision_id, output_content_hash, created_at, created_by,
    event_sequence
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(project_id),
    sqlc.arg(rule_id), sqlc.arg(rule_name), sqlc.arg(rule_revision),
    sqlc.arg(source_type), sqlc.arg(source_id), sqlc.arg(source_revision),
    sqlc.arg(source_content_hash), sqlc.arg(knowledge_revision_id),
    sqlc.arg(output_content_hash), sqlc.arg(created_at),
    sqlc.arg(created_by), sqlc.arg(event_sequence)
);

-- name: GetCuratorDerivationByKnowledge :one
SELECT id, workspace_id, project_id, rule_id, rule_name, rule_revision,
       source_type, source_id, source_revision, source_content_hash,
       knowledge_revision_id, output_content_hash, created_at, created_by,
       event_sequence
FROM curator_derivations
WHERE workspace_id = sqlc.arg(workspace_id)
  AND knowledge_revision_id = sqlc.arg(knowledge_revision_id);

-- name: ListCuratorQueueRevisionIDs :many
SELECT kr.id
FROM knowledge_revisions kr
JOIN knowledge_items ki ON ki.id = kr.item_id
WHERE ki.workspace_id = sqlc.arg(workspace_id)
  AND ki.project_id = sqlc.arg(project_id)
  AND kr.review_status = 'proposed'
  AND kr.currency_status = 'pending'
  AND (
      CAST(sqlc.arg(after_proposed_at) AS TEXT) = ''
      OR crewfold_timestamp_key(kr.proposed_at) > crewfold_timestamp_key(CAST(sqlc.arg(after_proposed_at) AS TEXT))
      OR (crewfold_timestamp_key(kr.proposed_at) = crewfold_timestamp_key(CAST(sqlc.arg(after_proposed_at) AS TEXT))
          AND kr.id > sqlc.arg(after_revision_id))
  )
ORDER BY crewfold_timestamp_key(kr.proposed_at), kr.id
LIMIT sqlc.arg(result_limit);

-- name: ListCuratorEligibleRevisionIDs :many
SELECT kr.id
FROM curator_derivations d
JOIN knowledge_revisions kr ON kr.id = d.knowledge_revision_id
JOIN knowledge_items ki ON ki.id = kr.item_id
WHERE d.workspace_id = sqlc.arg(workspace_id)
  AND d.project_id = sqlc.arg(project_id)
  AND d.rule_name = sqlc.arg(rule_name)
  AND d.event_sequence <= sqlc.arg(max_event_sequence)
  AND ki.workspace_id = d.workspace_id
  AND ki.project_id = d.project_id
  AND kr.review_status = 'proposed'
  AND kr.currency_status = 'pending'
ORDER BY crewfold_timestamp_key(kr.proposed_at), kr.id
LIMIT sqlc.arg(result_limit);

-- name: InsertCuratorAutoAcceptance :exec
INSERT INTO curator_auto_acceptances(
    id, workspace_id, project_id, rule_id, rule_name, rule_revision,
    derivation_id, knowledge_revision_id, authority_check_id,
    knowledge_event_sequence, event_sequence, created_at, actor_id, actor_type
) VALUES (
    sqlc.arg(id), sqlc.arg(workspace_id), sqlc.arg(project_id),
    sqlc.arg(rule_id), sqlc.arg(rule_name), sqlc.arg(rule_revision),
    sqlc.arg(derivation_id), sqlc.arg(knowledge_revision_id),
    sqlc.arg(authority_check_id), sqlc.arg(knowledge_event_sequence),
    sqlc.arg(event_sequence), sqlc.arg(created_at), sqlc.arg(actor_id),
    sqlc.arg(actor_type)
);

-- name: GetCuratorAutoAcceptanceByKnowledge :one
SELECT id, workspace_id, project_id, rule_id, rule_name, rule_revision,
       derivation_id, knowledge_revision_id, authority_check_id,
       knowledge_event_sequence, event_sequence, created_at, actor_id, actor_type
FROM curator_auto_acceptances
WHERE workspace_id = sqlc.arg(workspace_id)
  AND knowledge_revision_id = sqlc.arg(knowledge_revision_id);
