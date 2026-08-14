-- name: InsertDeliverableCommitment :exec
INSERT INTO deliverable_commitments(
 id,workspace_id,project_id,objective_id,task_id,commitment_key,title,description,
 acceptance_criteria_json,content_json,content_sha256,created_at,created_by
) VALUES(
 sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),sqlc.arg(objective_id),sqlc.arg(task_id),
 sqlc.arg(commitment_key),sqlc.arg(title),sqlc.arg(description),sqlc.arg(acceptance_criteria_json),
 sqlc.arg(content_json),sqlc.arg(content_sha256),sqlc.arg(created_at),'local-owner'
);

-- name: InsertOutcomeCommitmentReceipt :exec
INSERT INTO outcome_commitment_receipts(commitment_id,event_sequence,created_at)
VALUES(sqlc.arg(commitment_id),sqlc.arg(event_sequence),sqlc.arg(created_at));

-- name: GetDeliverableCommitment :one
SELECT commitment.* FROM deliverable_commitments commitment
WHERE commitment.workspace_id=sqlc.arg(workspace_id) AND commitment.id=sqlc.arg(id);

-- name: ListDeliverableCommitments :many
SELECT commitment.* FROM deliverable_commitments commitment
WHERE commitment.workspace_id=sqlc.arg(workspace_id)
 AND (CAST(sqlc.arg(project_id) AS TEXT)='' OR commitment.project_id=sqlc.arg(project_id))
 AND (CAST(sqlc.arg(objective_id) AS TEXT)='' OR commitment.objective_id=sqlc.arg(objective_id))
 AND (CAST(sqlc.arg(task_id) AS TEXT)='' OR commitment.task_id=sqlc.arg(task_id))
ORDER BY commitment.project_id,commitment.objective_id,commitment.task_id,commitment.commitment_key,commitment.id
LIMIT sqlc.arg(result_limit);

-- name: GetOutcomeCommitmentScope :one
SELECT task.workspace_id,task.project_id,task.objective_id,task.revision AS task_revision,
 objective.revision AS objective_revision,task.status AS task_status,objective.status AS objective_status
FROM tasks task JOIN objectives objective ON objective.id=task.objective_id
WHERE task.workspace_id=sqlc.arg(workspace_id) AND task.id=sqlc.arg(task_id)
 AND objective.workspace_id=task.workspace_id AND objective.project_id=task.project_id;

-- name: NextOutcomeAssessmentRevision :one
SELECT CAST(COALESCE(MAX(revision),0)+1 AS INTEGER) AS next_revision
FROM outcome_assessments WHERE commitment_id=sqlc.arg(commitment_id);

-- name: GetCurrentAcceptedOutcomeAssessmentID :one
SELECT id FROM outcome_assessments
WHERE commitment_id=sqlc.arg(commitment_id) AND review_state='accepted';

-- name: InsertOutcomeAssessment :exec
INSERT INTO outcome_assessments(
 id,workspace_id,project_id,objective_id,task_id,commitment_id,revision,state_revision,
 review_state,conclusion,delivered_scope_json,unmet_scope_json,content_json,content_sha256,
 supersedes_assessment_id,proposed_at,proposed_by,decided_at,decided_by,decision_note
) VALUES(
 sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),sqlc.arg(objective_id),sqlc.arg(task_id),
 sqlc.arg(commitment_id),sqlc.arg(revision),1,'proposed',sqlc.arg(conclusion),
 sqlc.arg(delivered_scope_json),sqlc.arg(unmet_scope_json),sqlc.arg(content_json),sqlc.arg(content_sha256),
 NULLIF(sqlc.arg(supersedes_assessment_id),''),sqlc.arg(proposed_at),'local-owner',NULL,NULL,NULL
);

-- name: InsertOutcomeAssessmentDecisionRef :exec
INSERT INTO outcome_assessment_decision_refs(
 assessment_id,ordinal,revision_id,content_sha256,event_sequence
) VALUES(sqlc.arg(assessment_id),sqlc.arg(ordinal),sqlc.arg(revision_id),sqlc.arg(content_sha256),sqlc.arg(event_sequence));

-- name: InsertOutcomeAssessmentEvidenceRef :exec
INSERT INTO outcome_assessment_evidence_refs(
 assessment_id,ordinal,source_type,source_id,source_revision,source_sha256,event_sequence,class,effect,pinned_freshness
) VALUES(
 sqlc.arg(assessment_id),sqlc.arg(ordinal),sqlc.arg(source_type),sqlc.arg(source_id),sqlc.arg(source_revision),
 sqlc.arg(source_sha256),sqlc.arg(event_sequence),sqlc.arg(class),sqlc.arg(effect),sqlc.arg(pinned_freshness)
);

-- name: InsertOutcomeAssessmentEffect :exec
INSERT INTO outcome_assessment_effects(assessment_id,ordinal,kind,direction,summary)
VALUES(sqlc.arg(assessment_id),sqlc.arg(ordinal),sqlc.arg(kind),sqlc.arg(direction),sqlc.arg(summary));

-- name: InsertOutcomeAssessmentDeviation :exec
INSERT INTO outcome_assessment_deviations(assessment_id,ordinal,kind,summary,related_task_id,related_task_revision)
VALUES(sqlc.arg(assessment_id),sqlc.arg(ordinal),sqlc.arg(kind),sqlc.arg(summary),NULLIF(sqlc.arg(related_task_id),''),NULLIF(sqlc.arg(related_task_revision),0));

-- name: InsertOutcomeAssessmentRisk :exec
INSERT INTO outcome_assessment_risks(assessment_id,ordinal,severity,summary,mitigation)
VALUES(sqlc.arg(assessment_id),sqlc.arg(ordinal),sqlc.arg(severity),sqlc.arg(summary),sqlc.arg(mitigation));

-- name: InsertOutcomeAssessmentUnknown :exec
INSERT INTO outcome_assessment_unknowns(assessment_id,ordinal,summary)
VALUES(sqlc.arg(assessment_id),sqlc.arg(ordinal),sqlc.arg(summary));

-- name: InsertOutcomeAssessmentFollowUpTask :exec
INSERT INTO outcome_assessment_follow_up_tasks(assessment_id,ordinal,task_id,task_revision,event_sequence)
VALUES(sqlc.arg(assessment_id),sqlc.arg(ordinal),sqlc.arg(task_id),sqlc.arg(task_revision),sqlc.arg(event_sequence));

-- name: InsertOutcomeAssessmentOwnerAttention :exec
INSERT INTO outcome_assessment_owner_attention(assessment_id,ordinal,urgency,action,reason)
VALUES(sqlc.arg(assessment_id),sqlc.arg(ordinal),sqlc.arg(urgency),sqlc.arg(action),sqlc.arg(reason));

-- name: InsertOutcomeAssessmentSubmission :exec
INSERT INTO outcome_assessment_submissions(assessment_id,event_sequence,child_count,submitted_at)
VALUES(sqlc.arg(assessment_id),sqlc.arg(event_sequence),sqlc.arg(child_count),sqlc.arg(submitted_at));

-- name: GetOutcomeAssessment :one
SELECT assessment.* FROM outcome_assessments assessment
WHERE assessment.workspace_id=sqlc.arg(workspace_id) AND assessment.id=sqlc.arg(id);

-- name: ListOutcomeAssessmentIDs :many
SELECT assessment.id FROM outcome_assessments assessment
WHERE assessment.workspace_id=sqlc.arg(workspace_id)
 AND (CAST(sqlc.arg(project_id) AS TEXT)='' OR assessment.project_id=sqlc.arg(project_id))
 AND (CAST(sqlc.arg(objective_id) AS TEXT)='' OR assessment.objective_id=sqlc.arg(objective_id))
 AND (CAST(sqlc.arg(task_id) AS TEXT)='' OR assessment.task_id=sqlc.arg(task_id))
 AND (CAST(sqlc.arg(commitment_id) AS TEXT)='' OR assessment.commitment_id=sqlc.arg(commitment_id))
 AND (CAST(sqlc.arg(review_state) AS TEXT)='' OR assessment.review_state=sqlc.arg(review_state))
 AND (CAST(sqlc.arg(conclusion) AS TEXT)='' OR assessment.conclusion=sqlc.arg(conclusion))
ORDER BY assessment.project_id,assessment.objective_id,assessment.task_id,assessment.commitment_id,assessment.revision
LIMIT sqlc.arg(result_limit);

-- name: ListOutcomeAssessmentDecisionRefs :many
SELECT * FROM outcome_assessment_decision_refs WHERE assessment_id=sqlc.arg(assessment_id) ORDER BY ordinal;

-- name: ListOutcomeAssessmentEvidenceRefs :many
SELECT * FROM outcome_assessment_evidence_refs WHERE assessment_id=sqlc.arg(assessment_id) ORDER BY ordinal;

-- name: ListOutcomeAssessmentEffects :many
SELECT * FROM outcome_assessment_effects WHERE assessment_id=sqlc.arg(assessment_id) ORDER BY ordinal;

-- name: ListOutcomeAssessmentDeviations :many
SELECT * FROM outcome_assessment_deviations WHERE assessment_id=sqlc.arg(assessment_id) ORDER BY ordinal;

-- name: ListOutcomeAssessmentRisks :many
SELECT * FROM outcome_assessment_risks WHERE assessment_id=sqlc.arg(assessment_id) ORDER BY ordinal;

-- name: ListOutcomeAssessmentUnknowns :many
SELECT * FROM outcome_assessment_unknowns WHERE assessment_id=sqlc.arg(assessment_id) ORDER BY ordinal;

-- name: ListOutcomeAssessmentFollowUpTasks :many
SELECT * FROM outcome_assessment_follow_up_tasks WHERE assessment_id=sqlc.arg(assessment_id) ORDER BY ordinal;

-- name: ListOutcomeAssessmentOwnerAttention :many
SELECT * FROM outcome_assessment_owner_attention WHERE assessment_id=sqlc.arg(assessment_id) ORDER BY ordinal;

-- name: GetAcceptedDecisionForOutcome :one
SELECT revision.id AS revision_id,revision.content_hash,revision.currency_status,revision.freshness_policy,
 revision.fresh_until,revision.state_revision,
 (SELECT event.sequence FROM events event WHERE event.workspace_id=item.workspace_id
   AND event.entity_type='knowledge_revision' AND event.entity_id=revision.id
   AND event.type='knowledge.accepted' ORDER BY event.sequence DESC LIMIT 1) AS event_sequence
FROM knowledge_revisions revision JOIN knowledge_items item ON item.id=revision.item_id
WHERE item.workspace_id=sqlc.arg(workspace_id) AND item.project_id=sqlc.arg(project_id)
 AND (item.task_scope_id IS NULL OR item.task_scope_id=sqlc.arg(task_id))
 AND item.type='decision' AND revision.id=sqlc.arg(revision_id) AND revision.review_status='accepted';

-- name: OutcomeDecisionHasOpenContradiction :one
SELECT EXISTS(
 SELECT 1 FROM knowledge_contradictions contradiction
 WHERE contradiction.workspace_id=sqlc.arg(workspace_id) AND contradiction.status='open'
  AND (contradiction.left_revision_id=sqlc.arg(revision_id) OR contradiction.right_revision_id=sqlc.arg(revision_id))
) AS disputed;

-- name: GetOutcomeHandoffEvidence :one
SELECT handoff.id,handoff.run_id,handoff.task_id,handoff.summary,handoff.evidence_json,handoff.created_at,
 run.revision AS run_revision,run.status AS run_status,
 (SELECT event.sequence FROM events event WHERE event.workspace_id=run.workspace_id AND event.entity_type='task'
   AND event.type='task.handoff_recorded' AND json_extract(event.data_json,'$.handoff_id')=handoff.id
   ORDER BY event.sequence DESC LIMIT 1) AS event_sequence,
 (SELECT event.entity_revision FROM events event WHERE event.workspace_id=run.workspace_id AND event.entity_type='task'
   AND event.type='task.handoff_recorded' AND json_extract(event.data_json,'$.handoff_id')=handoff.id
   ORDER BY event.sequence DESC LIMIT 1) AS source_revision
FROM run_handoffs handoff JOIN runs run ON run.id=handoff.run_id
WHERE run.workspace_id=sqlc.arg(workspace_id) AND handoff.id=sqlc.arg(handoff_id);

-- name: OutcomeHandoffIsIndependentReview :one
SELECT EXISTS(
 SELECT 1 FROM run_handoffs handoff
 JOIN manager_proposal_effects effect ON effect.entity_type='task' AND effect.entity_id=handoff.task_id AND effect.effect_type='created'
 JOIN manager_proposal_actions action ON action.id=effect.action_id AND action.proposal_id=effect.proposal_id AND action.type='request_review'
 JOIN manager_proposals proposal ON proposal.id=effect.proposal_id AND proposal.status='accepted'
 WHERE handoff.id=sqlc.arg(handoff_id) AND proposal.workspace_id=sqlc.arg(workspace_id)
   AND json_extract(action.payload_json,'$.task.task_id')=sqlc.arg(assessed_task_id)
) AS independent_review;

-- name: GetOutcomeCheckEvidence :one
SELECT evidence.id,evidence.requirement_id,evidence.requirement_revision,evidence.check_result_id,
 evidence.freshness_revision,evidence.class,evidence.effect,evidence.created_at,
 run.workspace_id,run.project_id,run.task_id,result.outcome,
 pinned.status AS pinned_freshness,current.status AS current_freshness,current.revision AS current_freshness_revision,
 requirement.status AS requirement_status,requirement.revision AS current_requirement_revision,
 (evidence.check_result_id=(
   SELECT latest_result.id FROM check_runs latest_run
   LEFT JOIN check_results latest_result ON latest_result.check_run_id=latest_run.id
   WHERE latest_run.requirement_id=evidence.requirement_id AND latest_run.requirement_revision=requirement.revision
   ORDER BY latest_run.created_at DESC,latest_run.id DESC LIMIT 1
 )) AS current_requirement_result,
 (SELECT event.sequence FROM events event WHERE event.workspace_id=run.workspace_id AND event.entity_type='check_result'
   AND event.entity_id=result.id AND event.entity_revision=evidence.freshness_revision
   AND event.type IN ('check.result_recorded','check.freshness_observed','check.freshness_stale')
   ORDER BY event.sequence DESC LIMIT 1) AS event_sequence
FROM check_requirement_evidence evidence
JOIN check_results result ON result.id=evidence.check_result_id
JOIN check_runs run ON run.id=result.check_run_id
JOIN task_check_requirements requirement ON requirement.id=evidence.requirement_id
JOIN check_result_freshness pinned ON pinned.check_result_id=result.id AND pinned.revision=evidence.freshness_revision
JOIN check_result_freshness current ON current.check_result_id=result.id
 AND current.revision=(SELECT MAX(latest.revision) FROM check_result_freshness latest WHERE latest.check_result_id=result.id)
WHERE run.workspace_id=sqlc.arg(workspace_id) AND evidence.id=sqlc.arg(evidence_id);

-- name: GetOutcomeCommitmentReceipt :one
SELECT receipt.* FROM outcome_commitment_receipts receipt WHERE receipt.commitment_id=sqlc.arg(commitment_id);

-- name: GetOutcomeAssessmentSubmission :one
SELECT submission.* FROM outcome_assessment_submissions submission WHERE submission.assessment_id=sqlc.arg(assessment_id);

-- name: GetOutcomeAssessmentGovernance :one
SELECT governance.* FROM outcome_assessment_governance governance WHERE governance.assessment_id=sqlc.arg(assessment_id);

-- name: GetOutcomeAssessmentSuccessorGovernance :one
SELECT successor.id AS successor_assessment_id,successor.commitment_id,successor.review_state,
 governance.decision_event_sequence,governance.superseded_event_sequence,governance.decided_at
FROM outcome_assessments successor
JOIN outcome_assessment_governance governance ON governance.assessment_id=successor.id AND governance.decision='accepted'
WHERE governance.superseded_assessment_id=sqlc.arg(assessment_id);

-- name: GetOutcomeTaskRevision :one
SELECT task.workspace_id,task.project_id,task.objective_id,task.revision,
 (SELECT event.sequence FROM events event WHERE event.workspace_id=task.workspace_id AND event.entity_type='task' AND event.entity_id=task.id AND event.entity_revision=task.revision ORDER BY event.sequence DESC LIMIT 1) AS event_sequence
FROM tasks task
WHERE task.workspace_id=sqlc.arg(workspace_id) AND task.id=sqlc.arg(task_id);

-- name: AcceptOutcomeAssessmentProjection :execrows
UPDATE outcome_assessments
SET review_state='accepted',state_revision=state_revision+1,decided_at=sqlc.arg(decided_at),decided_by='local-owner',decision_note=NULLIF(sqlc.arg(decision_note),'')
WHERE workspace_id=sqlc.arg(workspace_id) AND id=sqlc.arg(id) AND review_state='proposed' AND state_revision=sqlc.arg(expected_state_revision);

-- name: RejectOutcomeAssessmentProjection :execrows
UPDATE outcome_assessments
SET review_state='rejected',state_revision=state_revision+1,decided_at=sqlc.arg(decided_at),decided_by='local-owner',decision_note=NULLIF(sqlc.arg(decision_note),'')
WHERE workspace_id=sqlc.arg(workspace_id) AND id=sqlc.arg(id) AND review_state='proposed' AND state_revision=sqlc.arg(expected_state_revision);

-- name: SupersedeOutcomeAssessmentProjection :execrows
UPDATE outcome_assessments
SET review_state='superseded',state_revision=state_revision+1
WHERE workspace_id=sqlc.arg(workspace_id) AND id=sqlc.arg(id) AND review_state='accepted';

-- name: InsertOutcomeAssessmentGovernance :exec
INSERT INTO outcome_assessment_governance(
 assessment_id,decision,decision_event_sequence,superseded_assessment_id,superseded_event_sequence,decided_at
) VALUES(
 sqlc.arg(assessment_id),sqlc.arg(decision),sqlc.arg(decision_event_sequence),
 NULLIF(sqlc.arg(superseded_assessment_id),''),NULLIF(sqlc.arg(superseded_event_sequence),0),sqlc.arg(decided_at)
);

-- name: InsertOutcomeAssessmentAcceptanceBasis :exec
INSERT INTO outcome_assessment_acceptance_basis(assessment_id,event_sequence,source_sha256,created_at,created_by)
VALUES(sqlc.arg(assessment_id),sqlc.arg(event_sequence),sqlc.arg(source_sha256),sqlc.arg(created_at),'local-owner');

-- name: GetOutcomeAssessmentAcceptanceBasis :one
SELECT * FROM outcome_assessment_acceptance_basis WHERE assessment_id=sqlc.arg(assessment_id);

-- name: MaxWorkspaceEventSequence :one
SELECT CAST(COALESCE(MAX(sequence),0) AS INTEGER) AS event_sequence FROM events
WHERE workspace_id=sqlc.arg(workspace_id);

-- name: GetOutcomeJournalEvent :one
SELECT event.* FROM events event WHERE event.sequence=sqlc.arg(event_sequence);

-- name: InsertOwnerCheckpoint :exec
INSERT INTO owner_checkpoints(id,workspace_id,scope_type,scope_id,event_sequence,created_at,created_by)
VALUES(sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(scope_type),sqlc.arg(scope_id),sqlc.arg(event_sequence),sqlc.arg(created_at),'local-owner');

-- name: GetOwnerCheckpoint :one
SELECT * FROM owner_checkpoints WHERE workspace_id=sqlc.arg(workspace_id) AND id=sqlc.arg(id);

-- name: ListOwnerCheckpoints :many
SELECT * FROM owner_checkpoints WHERE workspace_id=sqlc.arg(workspace_id)
 AND (CAST(sqlc.arg(scope_type) AS TEXT)='' OR scope_type=sqlc.arg(scope_type))
 AND (CAST(sqlc.arg(scope_id) AS TEXT)='' OR scope_id=sqlc.arg(scope_id))
ORDER BY event_sequence DESC,id DESC LIMIT sqlc.arg(result_limit);

-- name: GetOutcomeScopeTask :one
SELECT workspace_id,project_id,objective_id,id FROM tasks
WHERE workspace_id=sqlc.arg(workspace_id) AND id=sqlc.arg(scope_id);

-- name: GetOutcomeScopeObjective :one
SELECT workspace_id,project_id,id FROM objectives
WHERE workspace_id=sqlc.arg(workspace_id) AND id=sqlc.arg(scope_id);

-- name: GetOutcomeScopeProject :one
SELECT workspace_id,id FROM projects
WHERE workspace_id=sqlc.arg(workspace_id) AND id=sqlc.arg(scope_id);

-- name: GetOutcomeScopeProjectByName :one
SELECT workspace_id,id FROM projects
WHERE workspace_id=sqlc.arg(workspace_id) AND name=sqlc.arg(scope_name);

-- name: GetOutcomeProjectorState :one
SELECT * FROM outcome_projector_state WHERE workspace_id=sqlc.arg(workspace_id);

-- name: InsertOutcomeProjectorState :exec
INSERT INTO outcome_projector_state(workspace_id,last_event_sequence,revision,updated_at)
VALUES(sqlc.arg(workspace_id),0,1,sqlc.arg(updated_at));

-- name: ListOutcomeProjectorEvents :many
SELECT sequence,type FROM events WHERE workspace_id=sqlc.arg(workspace_id)
 AND sequence>sqlc.arg(after_sequence) AND sequence<=sqlc.arg(cutoff_sequence)
ORDER BY sequence LIMIT 1000;

-- name: AdvanceOutcomeProjectorState :execrows
UPDATE outcome_projector_state SET last_event_sequence=sqlc.arg(last_event_sequence),revision=revision+1,updated_at=sqlc.arg(updated_at)
WHERE workspace_id=sqlc.arg(workspace_id) AND last_event_sequence=sqlc.arg(expected_event_sequence);

-- Candidate seed queries calculate a per-project row number for fair workspace
-- ordering, then apply a global 128-row cap per section. Each query is therefore
-- bounded independently of database and project cardinality before authenticated
-- detail expansion. section_total is counted before the cap and keeps omissions exact.

-- name: ListStaticManagementBriefingCandidateSeeds :many
WITH accepted AS (
 SELECT assessment.id,assessment.state_revision,assessment.content_sha256,assessment.project_id,
  assessment.objective_id,assessment.task_id,assessment.unmet_scope_json,
  governance.decision_event_sequence AS event_sequence,
  governance.decision_event_sequence>sqlc.arg(since_sequence) AS changed_since,
  governance.superseded_assessment_id,governance.superseded_event_sequence,
  prior.content_sha256 AS superseded_content_sha256,prior.state_revision AS superseded_state_revision
 FROM outcome_assessments assessment
 JOIN outcome_assessment_governance governance ON governance.assessment_id=assessment.id AND governance.decision='accepted'
 JOIN outcome_assessment_acceptance_basis basis ON basis.assessment_id=assessment.id AND basis.event_sequence=governance.decision_event_sequence
 LEFT JOIN outcome_assessments prior ON prior.id=governance.superseded_assessment_id
 WHERE assessment.workspace_id=sqlc.arg(workspace_id) AND assessment.review_state='accepted'
  AND governance.decision_event_sequence<=sqlc.arg(event_cursor)
  AND (CAST(sqlc.arg(project_id) AS TEXT)='' OR assessment.project_id=sqlc.arg(project_id))
  AND (CAST(sqlc.arg(objective_id) AS TEXT)='' OR assessment.objective_id=sqlc.arg(objective_id))
  AND (CAST(sqlc.arg(task_id) AS TEXT)='' OR assessment.task_id=sqlc.arg(task_id))
),
raw AS (
 SELECT 'required_decisions' AS section,attention.urgency AS urgency,accepted.project_id AS project_id,
  accepted.event_sequence AS source_event_sequence,'accepted_assessment' AS source_kind,
  accepted.id AS source_id,'attention' AS variant,attention.ordinal AS child_ordinal,
  accepted.state_revision AS source_revision,accepted.content_sha256 AS content_sha256,'' AS summary
 FROM accepted JOIN outcome_assessment_owner_attention attention ON attention.assessment_id=accepted.id
 UNION ALL
 SELECT 'required_decisions','next',accepted.project_id,max(accepted.event_sequence,follow_up.event_sequence),
  'accepted_assessment',accepted.id,'follow_up',follow_up.ordinal,accepted.state_revision,accepted.content_sha256,''
 FROM accepted JOIN outcome_assessment_follow_up_tasks follow_up ON follow_up.assessment_id=accepted.id
 UNION ALL
 SELECT 'risks_unknowns',CASE WHEN risk.severity IN ('critical','high') THEN 'now' WHEN risk.severity='medium' THEN 'next' ELSE 'later' END,
  accepted.project_id,accepted.event_sequence,'accepted_assessment',accepted.id,'risk',risk.ordinal,accepted.state_revision,accepted.content_sha256,''
 FROM accepted JOIN outcome_assessment_risks risk ON risk.assessment_id=accepted.id
 UNION ALL
 SELECT 'risks_unknowns','now',accepted.project_id,accepted.event_sequence,
  'accepted_assessment',accepted.id,'unknown',unknown.ordinal,accepted.state_revision,accepted.content_sha256,''
 FROM accepted JOIN outcome_assessment_unknowns unknown ON unknown.assessment_id=accepted.id
 UNION ALL
 SELECT 'deviations_unmet','next',accepted.project_id,accepted.event_sequence,
  'accepted_assessment',accepted.id,'deviation',deviation.ordinal,accepted.state_revision,accepted.content_sha256,''
 FROM accepted JOIN outcome_assessment_deviations deviation ON deviation.assessment_id=accepted.id
 UNION ALL
 SELECT 'deviations_unmet','now',accepted.project_id,accepted.event_sequence,
  'accepted_assessment',accepted.id,'unmet',-1,accepted.state_revision,accepted.content_sha256,''
 FROM accepted WHERE json_array_length(accepted.unmet_scope_json)<>0
 UNION ALL
 SELECT 'accepted_delivery','later',accepted.project_id,max(accepted.event_sequence,COALESCE((
   SELECT max(reference.event_sequence) FROM outcome_assessment_evidence_refs reference WHERE reference.assessment_id=accepted.id
  ),accepted.event_sequence)),
  'accepted_assessment',accepted.id,'delivery',-1,accepted.state_revision,accepted.content_sha256,''
 FROM accepted
 UNION ALL
 SELECT 'rationale_change','next',accepted.project_id,max(accepted.event_sequence,accepted.superseded_event_sequence),
  'accepted_assessment',accepted.id,'delivery_revised',-1,accepted.state_revision,accepted.content_sha256,''
 FROM accepted WHERE accepted.changed_since
  AND accepted.superseded_assessment_id IS NOT NULL AND accepted.superseded_event_sequence IS NOT NULL
  AND accepted.superseded_content_sha256 IS NOT NULL AND accepted.superseded_state_revision IS NOT NULL
 UNION ALL
 SELECT 'rationale_change','later',accepted.project_id,accepted.event_sequence,
  'accepted_assessment',accepted.id,'effect',effect.ordinal,accepted.state_revision,accepted.content_sha256,''
 FROM accepted JOIN outcome_assessment_effects effect ON effect.assessment_id=accepted.id
 WHERE accepted.changed_since
),
project_ranked AS (
 SELECT raw.section,raw.urgency,raw.project_id,raw.source_event_sequence,raw.source_kind,
  raw.source_id,raw.variant,raw.child_ordinal,raw.source_revision,raw.content_sha256,raw.summary,
  count(*) OVER (PARTITION BY raw.section) AS section_total,
  CASE raw.urgency WHEN 'now' THEN 0 WHEN 'next' THEN 1 ELSE 2 END AS urgency_order,
  row_number() OVER (
   PARTITION BY raw.section,raw.urgency,raw.project_id
   ORDER BY raw.source_event_sequence DESC,raw.source_kind,raw.source_id,raw.variant,raw.child_ordinal
  ) AS project_rank
 FROM raw
),
ranked AS (
 SELECT project_ranked.*,
  row_number() OVER (
   PARTITION BY section
   ORDER BY urgency_order,project_rank,project_id,source_event_sequence DESC,source_kind,source_id,variant,child_ordinal
  ) AS global_rank
 FROM project_ranked
)
SELECT section,urgency,project_id,source_event_sequence,source_kind,source_id,variant,
 child_ordinal,source_revision,content_sha256,summary,section_total
FROM ranked WHERE global_rank<=128
ORDER BY section,global_rank
LIMIT 896;

-- name: ListDecisionManagementBriefingCandidateSeeds :many
WITH accepted AS (
 SELECT assessment.id,assessment.state_revision,assessment.content_sha256,assessment.project_id,
  assessment.task_id,governance.decision_event_sequence AS event_sequence,
  governance.decision_event_sequence>sqlc.arg(since_sequence) AS changed_since
 FROM outcome_assessments assessment
 JOIN outcome_assessment_governance governance ON governance.assessment_id=assessment.id AND governance.decision='accepted'
 JOIN outcome_assessment_acceptance_basis basis ON basis.assessment_id=assessment.id AND basis.event_sequence=governance.decision_event_sequence
 WHERE assessment.workspace_id=sqlc.arg(workspace_id) AND assessment.review_state='accepted'
  AND governance.decision_event_sequence<=sqlc.arg(event_cursor)
  AND (CAST(sqlc.arg(project_id) AS TEXT)='' OR assessment.project_id=sqlc.arg(project_id))
  AND (CAST(sqlc.arg(objective_id) AS TEXT)='' OR assessment.objective_id=sqlc.arg(objective_id))
  AND (CAST(sqlc.arg(task_id) AS TEXT)='' OR assessment.task_id=sqlc.arg(task_id))
),
state AS (
 SELECT accepted.*,reference.ordinal,reference.revision_id,reference.content_sha256 AS decision_sha256,
  reference.event_sequence AS decision_event_sequence,
  EXISTS(
   SELECT 1 FROM knowledge_revisions revision JOIN knowledge_items item ON item.id=revision.item_id
   WHERE revision.id=reference.revision_id AND item.workspace_id=sqlc.arg(workspace_id)
    AND item.project_id=accepted.project_id AND (item.task_scope_id IS NULL OR item.task_scope_id=accepted.task_id)
    AND item.type='decision' AND revision.review_status='accepted'
    AND revision.content_hash=reference.content_sha256 AND revision.currency_status='current'
    AND (revision.freshness_policy<>'expires_at' OR
         (revision.fresh_until IS NOT NULL AND crewfold_timestamp_key(revision.fresh_until)>crewfold_timestamp_key(CAST(sqlc.arg(evaluated_at) AS TEXT))))
  ) AS is_current,
  EXISTS(
   SELECT 1 FROM knowledge_contradictions contradiction
   WHERE contradiction.workspace_id=sqlc.arg(workspace_id) AND contradiction.status='open'
    AND (contradiction.left_revision_id=reference.revision_id OR contradiction.right_revision_id=reference.revision_id)
  ) AS is_disputed
 FROM accepted JOIN outcome_assessment_decision_refs reference ON reference.assessment_id=accepted.id
),
raw AS (
 SELECT 'verification_gaps' AS section,'now' AS urgency,state.project_id AS project_id,
  CAST(max(state.event_sequence,state.decision_event_sequence) AS INTEGER) AS source_event_sequence,
  'accepted_assessment' AS source_kind,state.id AS source_id,'decision_gap' AS variant,
  state.ordinal AS child_ordinal,state.state_revision AS source_revision,
  state.content_sha256 AS content_sha256,'' AS summary
 FROM state WHERE NOT state.is_current OR state.is_disputed
 UNION ALL
 SELECT 'rationale_change','later',state.project_id,CAST(max(state.event_sequence,state.decision_event_sequence) AS INTEGER),
  'accepted_assessment',state.id,'decision',state.ordinal,state.state_revision,state.content_sha256,''
 FROM state WHERE state.changed_since
),
project_ranked AS (
 SELECT raw.section,raw.urgency,raw.project_id,raw.source_event_sequence,raw.source_kind,
  raw.source_id,raw.variant,raw.child_ordinal,raw.source_revision,raw.content_sha256,raw.summary,
  count(*) OVER (PARTITION BY raw.section) AS section_total,
  CASE raw.urgency WHEN 'now' THEN 0 WHEN 'next' THEN 1 ELSE 2 END AS urgency_order,
  row_number() OVER (
   PARTITION BY raw.section,raw.urgency,raw.project_id
   ORDER BY raw.source_event_sequence DESC,raw.source_kind,raw.source_id,raw.variant,raw.child_ordinal
  ) AS project_rank
 FROM raw
),
ranked AS (
 SELECT project_ranked.*,
  row_number() OVER (
   PARTITION BY section
   ORDER BY urgency_order,project_rank,project_id,source_event_sequence DESC,source_kind,source_id,variant,child_ordinal
  ) AS global_rank
 FROM project_ranked
)
SELECT section,urgency,project_id,source_event_sequence,source_kind,source_id,variant,
 child_ordinal,source_revision,content_sha256,summary,section_total
FROM ranked WHERE global_rank<=128
ORDER BY section,global_rank
LIMIT 896;

-- name: ListEvidenceManagementBriefingCandidateSeeds :many
WITH accepted AS (
 SELECT assessment.id,assessment.state_revision,assessment.content_sha256,assessment.project_id,
  assessment.task_id,governance.decision_event_sequence AS event_sequence
 FROM outcome_assessments assessment
 JOIN outcome_assessment_governance governance ON governance.assessment_id=assessment.id AND governance.decision='accepted'
 JOIN outcome_assessment_acceptance_basis basis ON basis.assessment_id=assessment.id AND basis.event_sequence=governance.decision_event_sequence
 WHERE assessment.workspace_id=sqlc.arg(workspace_id) AND assessment.review_state='accepted'
  AND governance.decision_event_sequence<=sqlc.arg(event_cursor)
  AND (CAST(sqlc.arg(project_id) AS TEXT)='' OR assessment.project_id=sqlc.arg(project_id))
  AND (CAST(sqlc.arg(objective_id) AS TEXT)='' OR assessment.objective_id=sqlc.arg(objective_id))
  AND (CAST(sqlc.arg(task_id) AS TEXT)='' OR assessment.task_id=sqlc.arg(task_id))
),
state AS (
 SELECT accepted.*,reference.ordinal,reference.event_sequence AS evidence_event_sequence,
  CASE reference.source_type
   WHEN 'handoff' THEN EXISTS(
    SELECT 1 FROM run_handoffs handoff JOIN runs run ON run.id=handoff.run_id
    WHERE handoff.id=reference.source_id AND run.workspace_id=sqlc.arg(workspace_id) AND run.status='completed'
     AND reference.source_sha256=lower(hex(sha256(CAST(json_object(
      'created_at',handoff.created_at,'evidence_json',json(handoff.evidence_json),'id',handoff.id,
      'run_id',handoff.run_id,'summary',handoff.summary,'task_id',handoff.task_id
     ) AS BLOB))))
     AND reference.event_sequence=(
      SELECT event.sequence FROM events event WHERE event.workspace_id=run.workspace_id
       AND event.entity_type='task' AND event.type='task.handoff_recorded'
       AND json_extract(event.data_json,'$.handoff_id')=handoff.id
      ORDER BY event.sequence DESC LIMIT 1)
     AND reference.source_revision=(
      SELECT event.entity_revision FROM events event WHERE event.workspace_id=run.workspace_id
       AND event.entity_type='task' AND event.type='task.handoff_recorded'
       AND json_extract(event.data_json,'$.handoff_id')=handoff.id
      ORDER BY event.sequence DESC LIMIT 1)
   )
   WHEN 'check_requirement_evidence' THEN EXISTS(
    SELECT 1 FROM check_requirement_evidence evidence
    JOIN check_results result ON result.id=evidence.check_result_id
    JOIN check_runs run ON run.id=result.check_run_id
    JOIN task_check_requirements requirement ON requirement.id=evidence.requirement_id
    JOIN check_result_freshness pinned ON pinned.check_result_id=result.id AND pinned.revision=evidence.freshness_revision
    JOIN check_result_freshness current_freshness ON current_freshness.check_result_id=result.id
     AND current_freshness.revision=(SELECT MAX(latest.revision) FROM check_result_freshness latest WHERE latest.check_result_id=result.id)
    WHERE evidence.id=reference.source_id AND run.workspace_id=sqlc.arg(workspace_id)
     AND run.task_id=accepted.task_id
     AND reference.source_sha256=lower(hex(sha256(CAST(json_object(
      'check_result_id',evidence.check_result_id,'class',evidence.class,'effect',evidence.effect,
      'freshness_revision',evidence.freshness_revision,'id',evidence.id,
      'pinned_freshness',pinned.status,'requirement_id',evidence.requirement_id,
      'requirement_revision',evidence.requirement_revision
     ) AS BLOB))))
     AND reference.event_sequence=(
      SELECT event.sequence FROM events event WHERE event.workspace_id=run.workspace_id
       AND event.entity_type='check_result' AND event.entity_id=result.id
       AND event.entity_revision=evidence.freshness_revision
       AND event.type IN ('check.result_recorded','check.freshness_observed','check.freshness_stale')
      ORDER BY event.sequence DESC LIMIT 1)
     AND requirement.status='active' AND requirement.revision=evidence.requirement_revision
     AND evidence.check_result_id=(
      SELECT latest_result.id FROM check_runs latest_run
      LEFT JOIN check_results latest_result ON latest_result.check_run_id=latest_run.id
      WHERE latest_run.requirement_id=evidence.requirement_id AND latest_run.requirement_revision=requirement.revision
      ORDER BY latest_run.created_at DESC,latest_run.id DESC LIMIT 1)
     AND current_freshness.revision=reference.source_revision AND current_freshness.status='fresh'
   )
   ELSE 0
  END AS is_current,
  reference.source_type='check_requirement_evidence' AND EXISTS(
   SELECT 1 FROM check_requirement_evidence evidence
   JOIN check_results result ON result.id=evidence.check_result_id
   JOIN check_runs run ON run.id=result.check_run_id
   JOIN task_check_requirements requirement ON requirement.id=evidence.requirement_id
   JOIN check_result_freshness current_freshness ON current_freshness.check_result_id=result.id
    AND current_freshness.revision=(SELECT MAX(latest.revision) FROM check_result_freshness latest WHERE latest.check_result_id=result.id)
   WHERE evidence.id=reference.source_id AND run.workspace_id=sqlc.arg(workspace_id)
    AND evidence.effect='contradicts' AND requirement.status='active'
    AND requirement.revision=evidence.requirement_revision
    AND evidence.check_result_id=(
     SELECT latest_result.id FROM check_runs latest_run
     LEFT JOIN check_results latest_result ON latest_result.check_run_id=latest_run.id
     WHERE latest_run.requirement_id=evidence.requirement_id AND latest_run.requirement_revision=requirement.revision
     ORDER BY latest_run.created_at DESC,latest_run.id DESC LIMIT 1)
    AND current_freshness.status='fresh'
  ) AS is_contradictory
 FROM accepted JOIN outcome_assessment_evidence_refs reference ON reference.assessment_id=accepted.id
),
raw AS (
 SELECT 'verification_gaps' AS section,'now' AS urgency,state.project_id,
  CAST(max(state.event_sequence,state.evidence_event_sequence) AS INTEGER) AS source_event_sequence,
  'accepted_assessment' AS source_kind,state.id AS source_id,'evidence_gap' AS variant,
  state.ordinal AS child_ordinal,state.state_revision AS source_revision,state.content_sha256,'' AS summary
 FROM state WHERE NOT state.is_current OR state.is_contradictory
),
project_ranked AS (
 SELECT raw.section,raw.urgency,raw.project_id,raw.source_event_sequence,raw.source_kind,
  raw.source_id,raw.variant,raw.child_ordinal,raw.source_revision,raw.content_sha256,raw.summary,
  count(*) OVER (PARTITION BY raw.section) AS section_total,
  CASE raw.urgency WHEN 'now' THEN 0 WHEN 'next' THEN 1 ELSE 2 END AS urgency_order,
  row_number() OVER (
   PARTITION BY raw.section,raw.urgency,raw.project_id
   ORDER BY raw.source_event_sequence DESC,raw.source_kind,raw.source_id,raw.variant,raw.child_ordinal
  ) AS project_rank
 FROM raw
),
ranked AS (
 SELECT project_ranked.*,
  row_number() OVER (
   PARTITION BY section
   ORDER BY urgency_order,project_rank,project_id,source_event_sequence DESC,source_kind,source_id,variant,child_ordinal
  ) AS global_rank
 FROM project_ranked
)
SELECT section,urgency,project_id,source_event_sequence,source_kind,source_id,variant,
 child_ordinal,source_revision,content_sha256,summary,section_total
FROM ranked WHERE global_rank<=128
ORDER BY section,global_rank
LIMIT 896;

-- name: ListUnassessedManagementBriefingCandidateSeeds :many
WITH raw AS (
 SELECT 'deviations_unmet' AS section,'now' AS urgency,commitment.project_id,
  receipt.event_sequence AS source_event_sequence,'unassessed_commitment' AS source_kind,
  commitment.id AS source_id,'unassessed' AS variant,-1 AS child_ordinal,1 AS source_revision,
  commitment.content_sha256,commitment.title AS summary
 FROM deliverable_commitments commitment
 JOIN outcome_commitment_receipts receipt ON receipt.commitment_id=commitment.id
 WHERE commitment.workspace_id=sqlc.arg(workspace_id) AND receipt.event_sequence<=sqlc.arg(event_cursor)
  AND (CAST(sqlc.arg(project_id) AS TEXT)='' OR commitment.project_id=sqlc.arg(project_id))
  AND (CAST(sqlc.arg(objective_id) AS TEXT)='' OR commitment.objective_id=sqlc.arg(objective_id))
  AND (CAST(sqlc.arg(task_id) AS TEXT)='' OR commitment.task_id=sqlc.arg(task_id))
  AND NOT EXISTS(SELECT 1 FROM outcome_assessments assessment
   WHERE assessment.commitment_id=commitment.id AND assessment.review_state='accepted')
),
project_ranked AS (
 SELECT raw.section,raw.urgency,raw.project_id,raw.source_event_sequence,raw.source_kind,
  raw.source_id,raw.variant,raw.child_ordinal,raw.source_revision,raw.content_sha256,raw.summary,
  count(*) OVER (PARTITION BY raw.section) AS section_total,
  CASE raw.urgency WHEN 'now' THEN 0 WHEN 'next' THEN 1 ELSE 2 END AS urgency_order,
  row_number() OVER (
   PARTITION BY raw.section,raw.urgency,raw.project_id
   ORDER BY raw.source_event_sequence DESC,raw.source_kind,raw.source_id,raw.variant,raw.child_ordinal
  ) AS project_rank
 FROM raw
),
ranked AS (
 SELECT project_ranked.*,
  row_number() OVER (
   PARTITION BY section
   ORDER BY urgency_order,project_rank,project_id,source_event_sequence DESC,source_kind,source_id,variant,child_ordinal
  ) AS global_rank
 FROM project_ranked
)
SELECT section,urgency,project_id,source_event_sequence,source_kind,source_id,variant,
 child_ordinal,source_revision,content_sha256,summary,section_total
FROM ranked WHERE global_rank<=128
ORDER BY section,global_rank
LIMIT 896;

-- name: ListContradictionManagementBriefingCandidateSeeds :many
WITH raw AS (
 SELECT 'contradictions' AS section,'now' AS urgency,contradiction.project_id,
  CAST(contradiction.confirm_event_sequence AS INTEGER) AS source_event_sequence,'open_contradiction' AS source_kind,
  contradiction.id AS source_id,'contradiction' AS variant,-1 AS child_ordinal,
  contradiction.state_revision AS source_revision,'' AS content_sha256,
  CAST(COALESCE(NULLIF(trim(contradiction.report_note),''),
   'Accepted knowledge revisions contradict: '||contradiction.left_revision_id||' and '||contradiction.right_revision_id) AS TEXT) AS summary
 FROM knowledge_contradictions contradiction
 WHERE contradiction.workspace_id=sqlc.arg(workspace_id) AND contradiction.status='open'
  AND contradiction.confirm_event_sequence<=CAST(sqlc.arg(event_cursor) AS INTEGER)
  AND (CAST(sqlc.arg(project_id) AS TEXT)='' OR contradiction.project_id=sqlc.arg(project_id))
  AND (
   (CAST(sqlc.arg(scope_type) AS TEXT)<>'task' AND CAST(sqlc.arg(scope_type) AS TEXT)<>'objective') OR EXISTS(
    SELECT 1 FROM knowledge_revisions revision JOIN knowledge_items item ON item.id=revision.item_id
    WHERE revision.id IN (contradiction.left_revision_id,contradiction.right_revision_id)
     AND item.workspace_id=sqlc.arg(workspace_id)
     AND ((CAST(sqlc.arg(scope_type) AS TEXT)='task' AND (item.task_scope_id IS NULL OR item.task_scope_id=CAST(sqlc.arg(task_id) AS TEXT)))
       OR (CAST(sqlc.arg(scope_type) AS TEXT)='objective' AND
           (item.task_scope_id IS NULL OR item.task_scope_id IN
            (SELECT task.id FROM tasks task WHERE task.objective_id=CAST(sqlc.arg(objective_id) AS TEXT)))))
   )
  )
),
project_ranked AS (
 SELECT raw.section,raw.urgency,raw.project_id,raw.source_event_sequence,raw.source_kind,
  raw.source_id,raw.variant,raw.child_ordinal,raw.source_revision,raw.content_sha256,raw.summary,
  count(*) OVER (PARTITION BY raw.section) AS section_total,
  CASE raw.urgency WHEN 'now' THEN 0 WHEN 'next' THEN 1 ELSE 2 END AS urgency_order,
  row_number() OVER (
   PARTITION BY raw.section,raw.urgency,raw.project_id
   ORDER BY raw.source_event_sequence DESC,raw.source_kind,raw.source_id,raw.variant,raw.child_ordinal
  ) AS project_rank
 FROM raw
),
ranked AS (
 SELECT project_ranked.*,
  row_number() OVER (
   PARTITION BY section
   ORDER BY urgency_order,project_rank,project_id,source_event_sequence DESC,source_kind,source_id,variant,child_ordinal
  ) AS global_rank
 FROM project_ranked
)
SELECT section,urgency,project_id,source_event_sequence,source_kind,source_id,variant,
 child_ordinal,source_revision,content_sha256,summary,section_total
FROM ranked WHERE global_rank<=128
ORDER BY section,global_rank
LIMIT 896;

-- name: GetAcceptedOutcomeAssessmentClaimSource :one
SELECT assessment.id AS assessment_id,assessment.state_revision,assessment.review_state,assessment.conclusion,assessment.content_sha256,
 assessment.project_id,assessment.objective_id,assessment.task_id,assessment.commitment_id,
 commitment.title AS commitment_title,commitment.content_sha256 AS commitment_sha256,
 receipt.event_sequence AS commitment_event_sequence,
 governance.decision_event_sequence AS event_sequence,basis.source_sha256 AS acceptance_basis_sha256,
 governance.superseded_assessment_id,governance.superseded_event_sequence,
 prior.content_sha256 AS superseded_content_sha256,prior.state_revision AS superseded_state_revision
FROM outcome_assessments assessment
JOIN deliverable_commitments commitment ON commitment.id=assessment.commitment_id
JOIN outcome_commitment_receipts receipt ON receipt.commitment_id=commitment.id
JOIN outcome_assessment_governance governance ON governance.assessment_id=assessment.id AND governance.decision='accepted'
JOIN outcome_assessment_acceptance_basis basis ON basis.assessment_id=assessment.id AND basis.event_sequence=governance.decision_event_sequence
LEFT JOIN outcome_assessments prior ON prior.id=governance.superseded_assessment_id
WHERE assessment.workspace_id=sqlc.arg(workspace_id) AND assessment.id=sqlc.arg(assessment_id)
 AND assessment.review_state='accepted' AND governance.decision_event_sequence<=sqlc.arg(event_cursor);

-- name: ListAssessmentAcceptedEvent :one
SELECT event.sequence FROM events event WHERE event.workspace_id=sqlc.arg(workspace_id)
 AND event.entity_type='outcome_assessment' AND event.entity_id=sqlc.arg(assessment_id)
 AND event.type='outcome.assessment_accepted' ORDER BY event.sequence DESC LIMIT 1;

-- name: NextManagementBriefingRevision :one
SELECT CAST(COALESCE(MAX(revision),0)+1 AS INTEGER) AS next_revision FROM management_briefings
WHERE workspace_id=sqlc.arg(workspace_id) AND scope_type=sqlc.arg(scope_type) AND scope_id=sqlc.arg(scope_id);

-- name: FindManagementBriefing :one
SELECT * FROM management_briefings WHERE workspace_id=sqlc.arg(workspace_id)
 AND scope_type=sqlc.arg(scope_type) AND scope_id=sqlc.arg(scope_id)
 AND event_cursor=sqlc.arg(event_cursor) AND checkpoint_id=sqlc.arg(checkpoint_id) AND content_sha256=sqlc.arg(content_sha256);

-- name: GetManagementBriefing :one
SELECT * FROM management_briefings WHERE workspace_id=sqlc.arg(workspace_id) AND id=sqlc.arg(id);

-- name: InsertManagementBriefing :exec
INSERT INTO management_briefings(
 id,revision,workspace_id,scope_type,scope_id,event_cursor,cutoff_event_sequence,checkpoint_id,
 since_event_sequence,evaluated_at,caught_up,unknown_event_type,unknown_event_sequence,
 content_json,content_sha256,byte_size,created_at
) VALUES(
 sqlc.arg(id),sqlc.arg(revision),sqlc.arg(workspace_id),sqlc.arg(scope_type),sqlc.arg(scope_id),
 sqlc.arg(event_cursor),sqlc.arg(cutoff_event_sequence),sqlc.arg(checkpoint_id),sqlc.arg(since_event_sequence),
 sqlc.arg(evaluated_at),sqlc.arg(caught_up),NULLIF(sqlc.arg(unknown_event_type),''),NULLIF(sqlc.arg(unknown_event_sequence),0),
 sqlc.arg(content_json),sqlc.arg(content_sha256),sqlc.arg(byte_size),sqlc.arg(created_at)
);

-- name: InsertManagementBriefingClaim :exec
INSERT INTO management_briefing_claims(
 briefing_id,ordinal,claim_id,semantic_key,kind,urgency,summary,status,project_id,source_event_sequence,claim_json
) VALUES(
 sqlc.arg(briefing_id),sqlc.arg(ordinal),sqlc.arg(claim_id),sqlc.arg(semantic_key),sqlc.arg(kind),sqlc.arg(urgency),
 sqlc.arg(summary),sqlc.arg(status),NULLIF(sqlc.arg(project_id),''),sqlc.arg(source_event_sequence),sqlc.arg(claim_json)
);

-- name: InsertManagementBriefingClaimSource :exec
INSERT INTO management_briefing_claim_sources(
 briefing_id,claim_id,ordinal,entity_type,entity_id,entity_revision,content_sha256,event_sequence,
 evidence_class,evidence_effect,pinned_freshness,current_freshness
) VALUES(
 sqlc.arg(briefing_id),sqlc.arg(claim_id),sqlc.arg(ordinal),sqlc.arg(entity_type),sqlc.arg(entity_id),
 sqlc.arg(entity_revision),sqlc.arg(content_sha256),sqlc.arg(event_sequence),
 sqlc.arg(evidence_class),sqlc.arg(evidence_effect),sqlc.arg(pinned_freshness),sqlc.arg(current_freshness)
);

-- name: InsertManagementBriefingReceipt :exec
INSERT INTO management_briefing_receipts(briefing_id,claim_count,source_count,sealed_at)
VALUES(sqlc.arg(briefing_id),sqlc.arg(claim_count),sqlc.arg(source_count),sqlc.arg(sealed_at));

-- name: GetManagementBriefingReceipt :one
SELECT * FROM management_briefing_receipts WHERE briefing_id=sqlc.arg(briefing_id);

-- name: ListManagementBriefingClaims :many
SELECT * FROM management_briefing_claims WHERE briefing_id=sqlc.arg(briefing_id) ORDER BY ordinal;

-- name: ListManagementBriefingClaimSourcesForBriefing :many
SELECT * FROM management_briefing_claim_sources
WHERE briefing_id=sqlc.arg(briefing_id) ORDER BY claim_id,ordinal;
