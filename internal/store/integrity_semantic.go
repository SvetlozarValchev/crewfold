package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"crewfold/internal/domain"
)

const maximumSemanticViolationSamples = 16

type semanticSQLCheck struct {
	name  string
	query string
}

var semanticFamilyChecks = map[string][]semanticSQLCheck{
	"core": {
		{name: "projection_and_receipt_envelopes", query: `
SELECT 'workspace:'||workspace.id AS sample FROM workspaces workspace
WHERE crewfold_timestamp_canonical(workspace.created_at)<>1
   OR crewfold_timestamp_canonical(workspace.updated_at)<>1
   OR workspace.updated_at<workspace.created_at
UNION ALL
SELECT 'idempotency:'||key FROM idempotency_keys
WHERE length(request_hash)<>64 OR request_hash GLOB '*[^0-9a-f]*'
   OR json_valid(response_json)<>1 OR crewfold_timestamp_canonical(created_at)<>1`},
	},
	"project": {
		{name: "scope_graph", query: `
SELECT 'project_repository:'||link.project_id||':'||link.repository_id AS sample
FROM project_repositories link
JOIN projects project ON project.id=link.project_id
JOIN repositories repository ON repository.id=link.repository_id
WHERE project.workspace_id<>repository.workspace_id
UNION ALL
SELECT 'checkout:'||checkout.id FROM checkouts checkout
JOIN projects project ON project.id=checkout.project_id
JOIN repositories repository ON repository.id=checkout.repository_id
LEFT JOIN project_repositories link ON link.project_id=checkout.project_id AND link.repository_id=checkout.repository_id
WHERE project.workspace_id<>repository.workspace_id OR link.project_id IS NULL
UNION ALL
SELECT 'objective:'||objective.id FROM objectives objective JOIN projects project ON project.id=objective.project_id
WHERE objective.workspace_id<>project.workspace_id
UNION ALL
SELECT 'task:'||task.id FROM tasks task JOIN projects project ON project.id=task.project_id
LEFT JOIN objectives objective ON objective.id=task.objective_id
WHERE task.workspace_id<>project.workspace_id
   OR (objective.id IS NOT NULL AND (objective.workspace_id<>task.workspace_id OR objective.project_id<>task.project_id))
UNION ALL
SELECT 'dependency:'||edge.task_id||':'||edge.depends_on_task_id FROM task_dependencies edge
JOIN tasks task ON task.id=edge.task_id JOIN tasks dependency ON dependency.id=edge.depends_on_task_id
WHERE task.workspace_id<>dependency.workspace_id OR task.project_id<>dependency.project_id
UNION ALL
SELECT 'assignment:'||assignment.id FROM task_assignments assignment
JOIN tasks task ON task.id=assignment.task_id JOIN agents agent ON agent.id=assignment.agent_id
WHERE task.workspace_id<>agent.workspace_id`},
		{name: "canonical_metadata", query: `
SELECT 'project:'||id AS sample FROM projects WHERE crewfold_timestamp_canonical(created_at)<>1 OR crewfold_timestamp_canonical(updated_at)<>1
UNION ALL SELECT 'repository:'||id FROM repositories WHERE crewfold_timestamp_canonical(created_at)<>1 OR crewfold_timestamp_canonical(updated_at)<>1 OR json_type(root_commits_json)<>'array'
UNION ALL SELECT 'checkout:'||id FROM checkouts WHERE crewfold_timestamp_canonical(created_at)<>1 OR crewfold_timestamp_canonical(updated_at)<>1 OR crewfold_timestamp_canonical(observed_at)<>1 OR json_type(dirty_paths_json)<>'array'
UNION ALL SELECT 'agent:'||id FROM agents WHERE crewfold_timestamp_canonical(created_at)<>1 OR crewfold_timestamp_canonical(updated_at)<>1
UNION ALL SELECT 'objective:'||id FROM objectives WHERE crewfold_timestamp_canonical(created_at)<>1 OR crewfold_timestamp_canonical(updated_at)<>1
UNION ALL SELECT 'task:'||id FROM tasks WHERE crewfold_timestamp_canonical(created_at)<>1 OR crewfold_timestamp_canonical(updated_at)<>1
UNION ALL SELECT 'assignment:'||id FROM task_assignments WHERE crewfold_timestamp_canonical(created_at)<>1 OR crewfold_timestamp_canonical(updated_at)<>1 OR crewfold_timestamp_canonical(lease_expires_at)<>1`},
	},
	"run": {
		{name: "lifecycle_job_and_binding_parity", query: `
SELECT 'missing_job:'||run.id AS sample FROM runs run LEFT JOIN run_jobs job ON job.run_id=run.id WHERE job.run_id IS NULL
UNION ALL
SELECT 'job_lease:'||job.run_id FROM run_jobs job
WHERE (job.status='leased')<>(job.lease_expires_at IS NOT NULL)
   OR crewfold_timestamp_canonical(job.available_at)<>1 OR crewfold_timestamp_canonical(job.created_at)<>1 OR crewfold_timestamp_canonical(job.updated_at)<>1
UNION ALL
SELECT 'terminal:'||run.id FROM runs run JOIN run_jobs job ON job.run_id=run.id
WHERE run.status IN ('stopped','review','completed','start_failed','failed')
  AND (job.status<>'complete' OR run.finished_at IS NULL)
UNION ALL
SELECT 'unfinished_timestamp:'||run.id FROM runs run
WHERE (run.status IN ('requested','starting','active','blocked','stopping','lost') AND run.finished_at IS NOT NULL)
   OR crewfold_timestamp_canonical(run.created_at)<>1 OR crewfold_timestamp_canonical(run.updated_at)<>1
   OR (run.started_at IS NOT NULL AND crewfold_timestamp_canonical(run.started_at)<>1)
   OR (run.finished_at IS NOT NULL AND crewfold_timestamp_canonical(run.finished_at)<>1)
UNION ALL
SELECT 'binding:'||binding.run_id FROM run_context_bindings binding
JOIN runs run ON run.id=binding.run_id JOIN context_packets packet ON packet.id=binding.context_packet_id
WHERE run.workspace_id<>packet.workspace_id OR run.project_id<>packet.project_id OR run.task_id<>packet.task_id
   OR run.agent_id<>packet.agent_id OR run.checkout_id<>packet.checkout_id
UNION ALL
SELECT 'run_scope:'||run.id FROM runs run
JOIN tasks task ON task.id=run.task_id JOIN agents agent ON agent.id=run.agent_id
JOIN checkouts checkout ON checkout.id=run.checkout_id
WHERE run.workspace_id<>task.workspace_id OR run.project_id<>task.project_id OR run.workspace_id<>agent.workspace_id
   OR run.project_id<>checkout.project_id OR run.assignment_id IS NULL`},
		{name: "runtime_binding_parity", query: `
SELECT 'run_runtime_binding:'||run.id AS sample FROM runs run
LEFT JOIN run_runtime_bindings binding ON binding.run_id=run.id
WHERE (run.status IN ('active','blocked','stopping') AND (binding.run_id IS NULL OR binding.provider_handle IS NULL))
   OR (binding.run_id IS NOT NULL AND (binding.operation_id<>run.id OR run.status NOT IN ('starting','active','blocked','stopping','lost')))
   OR (run.status='starting' AND binding.provider_handle IS NOT NULL)
   OR (binding.run_id IS NOT NULL AND (crewfold_timestamp_canonical(binding.created_at)<>1 OR crewfold_timestamp_canonical(binding.updated_at)<>1))`},
		{name: "immutable_receipts_and_hashes", query: `
SELECT 'run_log:'||log.id AS sample FROM run_log_artifacts log
JOIN runs run ON run.id=log.run_id LEFT JOIN immutable_artifacts artifact ON artifact.content_sha256=log.content_sha256
WHERE run.status NOT IN ('stopped','review','completed','start_failed','failed') OR artifact.content_sha256 IS NULL
   OR artifact.byte_size<>log.captured_bytes OR log.created_by<>'subsystem:run-worker'
UNION ALL
SELECT 'loss_resolution:'||receipt.run_id FROM run_loss_resolutions receipt
JOIN runs run ON run.id=receipt.run_id LEFT JOIN events event ON event.sequence=receipt.event_sequence
WHERE run.status<>'failed' OR run.revision<>receipt.lost_revision+1 OR run.failure_code<>'runtime_retired_by_owner'
   OR run.finished_at<>receipt.resolved_at OR run.updated_at<>receipt.resolved_at OR run.updated_by<>'local-owner'
   OR event.type<>'run.lost_resolved' OR event.entity_type<>'run' OR event.entity_id<>receipt.run_id
   OR event.entity_revision<>receipt.lost_revision+1 OR event.actor_id<>'local-owner' OR event.actor_type<>'human'
   OR json_extract(event.data_json,'$.resolution')<>receipt.resolution
   OR json_extract(event.data_json,'$.lost_revision')<>receipt.lost_revision
   OR json_extract(event.data_json,'$.note')<>receipt.note OR json_extract(event.data_json,'$.capacity_released')<>1
UNION ALL
SELECT 'context_packet:'||packet.id FROM context_packets packet
WHERE length(CAST(packet.packet_json AS BLOB))<>packet.byte_size
   OR packet.content_hash<>'sha256:'||lower(hex(sha256(CAST(json_set(packet.packet_json,
      '$.id','', '$.content_hash','', '$.created_at','', '$.created_by','', '$.byte_size',0,
      '$.budget.total.used_bytes',0, '$.budget.total.remaining_bytes',32768) AS BLOB))))
UNION ALL
SELECT 'run_artifact:'||artifact.id FROM run_artifacts artifact
WHERE artifact.byte_size<>length(CAST(artifact.content AS BLOB))
   OR artifact.content_hash<>'sha256:'||lower(hex(sha256(CAST(artifact.content AS BLOB))))
UNION ALL
SELECT 'run_report:'||report.id FROM run_reports report
WHERE (report.status='pending' AND report.applied_at IS NOT NULL)
   OR (report.status='applied' AND report.applied_at IS NULL)`},
		{name: "terminal_log_receipts", query: `
SELECT 'terminal_logs:'||run.id AS sample
FROM runs run
LEFT JOIN events event ON event.sequence=(
 SELECT max(candidate.sequence) FROM events candidate
 WHERE candidate.entity_type='run' AND candidate.entity_id=run.id
   AND candidate.type=CASE
    WHEN run.status='stopped' THEN 'run.stopped'
    WHEN run.status='review' THEN 'run.completion_proposed'
    WHEN run.status='completed' THEN 'run.completed'
    WHEN run.status='start_failed' THEN 'run.start_failed'
    WHEN run.status='failed' AND run.failure_code='runtime_retired_by_owner' THEN 'run.lost_resolved'
    WHEN run.status='failed' THEN 'run.failed'
   END
)
WHERE run.status IN ('stopped','review','completed','start_failed','failed')
 AND (event.sequence IS NULL OR event.entity_revision<>run.revision OR NOT (
  (json_type(event.data_json,'$.logs_available')='true'
   AND json_type(event.data_json,'$.logs_unavailable_reason') IS NULL
   AND json_type(event.data_json,'$.stdout_sha256')='text'
   AND json_type(event.data_json,'$.stderr_sha256')='text'
   AND length(json_extract(event.data_json,'$.stdout_sha256'))=64
   AND length(json_extract(event.data_json,'$.stderr_sha256'))=64
   AND json_extract(event.data_json,'$.stdout_sha256') NOT GLOB '*[^0-9a-f]*'
   AND json_extract(event.data_json,'$.stderr_sha256') NOT GLOB '*[^0-9a-f]*'
   AND (SELECT count(*) FROM run_log_artifacts log WHERE log.run_id=run.id)=2
   AND (SELECT count(DISTINCT log.kind) FROM run_log_artifacts log WHERE log.run_id=run.id)=2
   AND json_extract(event.data_json,'$.stdout_sha256')=(SELECT log.content_sha256 FROM run_log_artifacts log WHERE log.run_id=run.id AND log.kind='stdout')
   AND json_extract(event.data_json,'$.stderr_sha256')=(SELECT log.content_sha256 FROM run_log_artifacts log WHERE log.run_id=run.id AND log.kind='stderr'))
  OR
  (json_type(event.data_json,'$.logs_available')='false'
   AND json_type(event.data_json,'$.logs_unavailable_reason')='text'
   AND length(CAST(json_extract(event.data_json,'$.logs_unavailable_reason') AS BLOB)) BETWEEN 1 AND 2048
   AND instr(json_extract(event.data_json,'$.logs_unavailable_reason'),char(0))=0
   AND crewfold_utf8_valid(json_extract(event.data_json,'$.logs_unavailable_reason'))=1
   AND json_type(event.data_json,'$.stdout_sha256') IS NULL
   AND json_type(event.data_json,'$.stderr_sha256') IS NULL
   AND (SELECT count(*) FROM run_log_artifacts log WHERE log.run_id=run.id)=0)
 ))`},
	},
	"coordination": {
		{name: "claim_overlap_graph", query: `
SELECT 'claim:'||claim.id AS sample FROM work_claims claim
JOIN tasks task ON task.id=claim.task_id JOIN projects project ON project.id=claim.project_id
LEFT JOIN checkouts checkout ON checkout.id=claim.checkout_id
WHERE claim.workspace_id<>task.workspace_id OR claim.project_id<>task.project_id OR project.workspace_id<>claim.workspace_id
   OR (checkout.id IS NOT NULL AND checkout.project_id<>claim.project_id)
UNION ALL
SELECT 'overlap:'||overlap.id FROM work_overlaps overlap
JOIN work_claims low ON low.id=overlap.claim_low_id JOIN work_claims high ON high.id=overlap.claim_high_id
WHERE overlap.workspace_id<>low.workspace_id OR overlap.workspace_id<>high.workspace_id
   OR overlap.project_id<>low.project_id OR overlap.project_id<>high.project_id
   OR overlap.task_low_id<>low.task_id OR overlap.task_high_id<>high.task_id
   OR overlap.kind<>low.kind OR overlap.kind<>high.kind
   OR (overlap.status='open' AND overlap.resolved_at IS NOT NULL)
   OR (overlap.status='resolved' AND (overlap.resolved_at IS NULL OR overlap.resolution_reason IS NULL))
UNION ALL
SELECT 'hold:'||hold.overlap_id||':'||hold.task_id FROM task_coordination_holds hold
JOIN work_overlaps overlap ON overlap.id=hold.overlap_id
WHERE hold.task_id NOT IN (overlap.task_low_id,overlap.task_high_id) OR overlap.status<>'open'
UNION ALL
SELECT 'drift:'||drift.id FROM claim_drifts drift JOIN work_claims claim ON claim.id=drift.claim_id
WHERE drift.workspace_id<>claim.workspace_id OR drift.project_id<>claim.project_id OR drift.task_id<>claim.task_id
   OR (drift.status='open' AND drift.resolved_at IS NOT NULL) OR (drift.status='resolved' AND drift.resolved_at IS NULL)`},
	},
	"meeting": {
		{name: "participant_action_graph", query: `
SELECT 'meeting:'||meeting.id AS sample FROM meetings meeting
JOIN work_overlaps overlap ON overlap.id=meeting.overlap_id JOIN agents facilitator ON facilitator.id=meeting.facilitator_agent_id
WHERE meeting.workspace_id<>overlap.workspace_id OR meeting.project_id<>overlap.project_id OR facilitator.workspace_id<>meeting.workspace_id
   OR crewfold_timestamp_canonical(meeting.created_at)<>1 OR crewfold_timestamp_canonical(meeting.updated_at)<>1 OR crewfold_timestamp_canonical(meeting.deadline_at)<>1
UNION ALL
SELECT 'participant:'||participant.meeting_id||':'||participant.agent_id FROM meeting_participants participant
JOIN meetings meeting ON meeting.id=participant.meeting_id JOIN agents agent ON agent.id=participant.agent_id
LEFT JOIN tasks task ON task.id=participant.task_id
WHERE agent.workspace_id<>meeting.workspace_id OR (task.id IS NOT NULL AND (task.workspace_id<>meeting.workspace_id OR task.project_id<>meeting.project_id))
UNION ALL
SELECT 'contribution:'||contribution.id FROM meeting_contributions contribution
LEFT JOIN meeting_participants participant ON participant.meeting_id=contribution.meeting_id AND participant.agent_id=contribution.agent_id
WHERE participant.meeting_id IS NULL
UNION ALL
SELECT 'action:'||action.id FROM meeting_actions action JOIN meeting_proposals proposal ON proposal.id=action.proposal_id
WHERE (action.status='pending' AND (action.result_entity_id IS NOT NULL OR action.diagnostic IS NOT NULL OR action.applied_at IS NOT NULL))
   OR (action.status='applied' AND action.applied_at IS NULL)
   OR (action.status='failed' AND (action.diagnostic IS NULL OR action.applied_at IS NULL))`},
		{name: "contiguous_children", query: `
SELECT 'participants:'||meeting_id AS sample FROM meeting_participants GROUP BY meeting_id HAVING min(ordinal)<>0 OR max(ordinal)+1<>count(*)
UNION ALL SELECT 'actions:'||proposal_id FROM meeting_actions GROUP BY proposal_id HAVING min(ordinal)<>0 OR max(ordinal)+1<>count(*)`},
	},
	"knowledge": {
		{name: "content_scope_and_authority_graph", query: `
SELECT 'revision_hash:'||revision.id AS sample FROM knowledge_revisions revision
WHERE revision.content_hash<>lower(hex(sha256(CAST(revision.title||char(10)||revision.body AS BLOB))))
   OR crewfold_timestamp_canonical(revision.proposed_at)<>1
   OR (revision.accepted_at IS NOT NULL AND crewfold_timestamp_canonical(revision.accepted_at)<>1)
   OR (revision.rejected_at IS NOT NULL AND crewfold_timestamp_canonical(revision.rejected_at)<>1)
   OR (revision.stale_at IS NOT NULL AND crewfold_timestamp_canonical(revision.stale_at)<>1)
UNION ALL
SELECT 'item_scope:'||item.id FROM knowledge_items item
LEFT JOIN knowledge_item_task_scopes scope ON scope.item_id=item.id
LEFT JOIN knowledge_task_scope_anchors anchor ON anchor.task_id=scope.task_id
WHERE (item.task_scope_id IS NULL)<>(scope.item_id IS NULL)
   OR (item.task_scope_id IS NOT NULL AND (anchor.task_id IS NULL OR anchor.task_id<>item.task_scope_id OR anchor.workspace_id<>item.workspace_id OR anchor.project_id<>item.project_id))
UNION ALL
SELECT 'native_anchor:'||anchor.task_id FROM knowledge_task_scope_anchors anchor JOIN tasks task ON task.id=anchor.task_id
WHERE anchor.workspace_id<>task.workspace_id OR anchor.project_id<>task.project_id OR anchor.created_at<>task.created_at OR anchor.created_by<>task.created_by
UNION ALL
SELECT 'accepted_authority:'||revision.id FROM knowledge_revisions revision JOIN knowledge_items item ON item.id=revision.item_id
WHERE revision.review_status='accepted'
 AND NOT EXISTS(SELECT 1 FROM knowledge_authority_checks authority JOIN events event ON event.sequence=authority.event_sequence
   WHERE authority.revision_id=revision.id AND authority.action='accept' AND authority.outcome='allowed'
     AND event.type='knowledge.accepted' AND event.workspace_id=item.workspace_id AND event.entity_id=revision.id AND event.entity_revision<=revision.state_revision)
 AND NOT EXISTS(SELECT 1 FROM knowledge_import_entities imported WHERE imported.entity_type='knowledge_revision' AND imported.entity_id=revision.id)
UNION ALL
SELECT 'contradiction_event:'||contradiction.id FROM knowledge_contradictions contradiction
LEFT JOIN events detected ON detected.sequence=contradiction.detected_event_sequence
WHERE detected.type NOT IN ('contradiction.detected','contradiction.imported') OR detected.entity_id<>contradiction.id OR detected.workspace_id<>contradiction.workspace_id
   OR (contradiction.status IN ('open','resolved') AND NOT EXISTS(SELECT 1 FROM knowledge_contradiction_authority_checks authority
       WHERE authority.contradiction_id=contradiction.id AND authority.action='confirm' AND authority.outcome='allowed'))
UNION ALL
SELECT 'import_receipt:'||imported.id FROM knowledge_imports imported LEFT JOIN events event ON event.sequence=imported.completed_event_sequence
WHERE event.type<>'knowledge.import_completed' OR event.workspace_id<>imported.workspace_id OR event.entity_type<>'knowledge_import' OR event.entity_id<>imported.id`},
		{name: "contiguous_sources", query: `
SELECT 'sources:'||revision_id AS sample FROM knowledge_sources GROUP BY revision_id HAVING min(ordinal)<>0 OR max(ordinal)+1<>count(*)`},
	},
	"context": {
		{name: "delta_chain_and_seal", query: `
SELECT 'state:'||state.run_id AS sample FROM run_context_delta_state state
LEFT JOIN run_context_bindings binding ON binding.run_id=state.run_id
WHERE binding.context_packet_id IS NULL OR binding.context_packet_id<>state.context_packet_id
   OR state.delta_count<>state.last_sequence
   OR state.cumulative_byte_size<>(SELECT COALESCE(sum(delta.byte_size),0) FROM context_deltas delta WHERE delta.run_id=state.run_id AND delta.sequence<=state.last_sequence)
   OR (state.last_sequence=0)<>(state.last_delta_id IS NULL)
   OR (state.status='pending_ack')<>(state.pending_delta_id IS NOT NULL)
UNION ALL
SELECT 'delta:'||delta.id FROM context_deltas delta
WHERE length(CAST(delta.delta_json AS BLOB))<>delta.byte_size
   OR json_extract(delta.delta_json,'$.id')<>delta.id OR json_extract(delta.delta_json,'$.run_id')<>delta.run_id
   OR json_extract(delta.delta_json,'$.context_packet_id')<>delta.context_packet_id OR json_extract(delta.delta_json,'$.sequence')<>delta.sequence
   OR json_extract(delta.delta_json,'$.content_hash')<>delta.content_hash OR json_extract(delta.delta_json,'$.byte_size')<>delta.byte_size
   OR crewfold_timestamp_canonical(delta.created_at)<>1
   OR (delta.sequence=1 AND delta.parent_delta_id IS NOT NULL)
   OR (delta.sequence>1 AND NOT EXISTS(SELECT 1 FROM context_deltas parent WHERE parent.run_id=delta.run_id AND parent.sequence=delta.sequence-1 AND parent.id=delta.parent_delta_id))
   OR NOT EXISTS(SELECT 1 FROM events event WHERE event.sequence=delta.built_event_sequence AND event.type='context_delta.built'
      AND event.entity_type='context_delta' AND event.entity_id=delta.id AND event.entity_revision=delta.sequence)
UNION ALL
SELECT 'ack:'||ack.id FROM context_delta_acknowledgements ack JOIN context_deltas delta ON delta.id=ack.delta_id
LEFT JOIN events event ON event.sequence=ack.event_sequence
WHERE ack.run_id<>delta.run_id OR ack.context_packet_id<>delta.context_packet_id OR ack.sequence<>delta.sequence
   OR event.type<>'context_delta.acknowledged' OR event.entity_type<>'context_delta' OR event.entity_id<>delta.id
   OR event.entity_revision<>delta.sequence OR event.occurred_at<>ack.acknowledged_at`},
	},
	"management": {
		{name: "sealed_content_and_child_counts", query: `
SELECT 'grant:'||grant_row.id AS sample FROM manager_grants grant_row
WHERE grant_row.content_sha256<>lower(hex(sha256(CAST(grant_row.content_json AS BLOB))))
   OR json_array_length(grant_row.proposal_kinds_json)<>(SELECT count(*) FROM manager_grant_proposal_kinds child WHERE child.grant_id=grant_row.id)
   OR json_array_length(grant_row.launch_profiles_json)<>(SELECT count(*) FROM manager_grant_launch_profiles child WHERE child.grant_id=grant_row.id)
   OR json_array_length(grant_row.allowed_claim_kinds_json)<>(SELECT count(*) FROM manager_grant_claim_kinds child WHERE child.grant_id=grant_row.id)
UNION ALL
SELECT 'profile:'||profile.id FROM launch_profiles profile
WHERE profile.scenario_sha256<>lower(hex(sha256(CAST(profile.scenario_json AS BLOB))))
   OR profile.content_sha256<>lower(hex(sha256(CAST(profile.content_json AS BLOB))))
UNION ALL
SELECT 'proposal:'||proposal.id FROM manager_proposals proposal
LEFT JOIN manager_proposal_submissions submission ON submission.proposal_id=proposal.id
WHERE proposal.content_sha256<>lower(hex(sha256(CAST(proposal.content_json AS BLOB))))
   OR submission.proposal_id IS NULL OR submission.action_count<>(SELECT count(*) FROM manager_proposal_actions action WHERE action.proposal_id=proposal.id)
UNION ALL
SELECT 'proposal_decision:'||decision.proposal_id FROM manager_proposal_decisions decision
JOIN manager_proposals proposal ON proposal.id=decision.proposal_id
WHERE proposal.status<>decision.status OR proposal.revision<>decision.proposal_revision
   OR decision.effect_count<>(SELECT count(*) FROM manager_proposal_effects effect WHERE effect.proposal_id=decision.proposal_id)
UNION ALL
SELECT 'action_receipt:'||receipt.action_id FROM supervisor_action_receipts receipt
JOIN supervisor_actions action ON action.id=receipt.action_id LEFT JOIN events event ON event.sequence=receipt.event_sequence
WHERE receipt.workspace_id<>action.workspace_id OR receipt.condition_key<>action.condition_key
   OR event.type<>'supervisor.action_recorded' OR event.entity_type<>'supervisor_action' OR event.entity_id<>action.id
UNION ALL
SELECT 'approval:'||approval.id FROM approval_requests approval JOIN supervisor_actions action ON action.id=approval.action_id
WHERE action.approval_id<>approval.id OR (approval.status IN ('denied','expired','consumed') AND approval.decision_event_sequence IS NULL)
UNION ALL
SELECT 'owner_conversation:'||conversation.id FROM owner_conversations conversation
JOIN projects project ON project.id=conversation.project_id
WHERE project.workspace_id<>conversation.workspace_id
UNION ALL
SELECT 'owner_turn:'||turn.id FROM owner_turns turn
WHERE turn.plan_sha256<>lower(hex(sha256(CAST(turn.plan_json AS BLOB))))
   OR json_array_length(turn.plan_json)<>(SELECT count(*) FROM owner_turn_operations operation WHERE operation.turn_id=turn.id)
   OR (turn.kind='query' AND (turn.status<>'completed' OR turn.answer IS NULL OR json_array_length(turn.plan_json)<>0))
   OR (turn.initiated_by='owner' AND (turn.kind='review' OR turn.trigger_event_sequence IS NOT NULL))
   OR (turn.initiated_by='manager' AND (turn.kind<>'review' OR turn.trigger_event_sequence<>turn.as_of_event_sequence))
	OR (turn.kind IN ('plan','review') AND turn.status='planned' AND EXISTS(SELECT 1 FROM owner_turn_operations operation WHERE operation.turn_id=turn.id AND operation.status<>'pending'))
	OR (turn.status='completed' AND EXISTS(SELECT 1 FROM owner_turn_operations operation WHERE operation.turn_id=turn.id AND operation.status<>'applied'))
UNION ALL
SELECT 'owner_manager_review:'||job.project_id FROM owner_manager_review_jobs job
JOIN projects project ON project.id=job.project_id
JOIN owner_conversations conversation ON conversation.id=job.conversation_id
LEFT JOIN owner_turns turn ON turn.id=job.last_turn_id
WHERE project.workspace_id<>job.workspace_id OR conversation.workspace_id<>job.workspace_id OR conversation.project_id<>job.project_id
   OR (job.status='idle' AND (turn.id IS NULL OR turn.initiated_by<>'manager' OR turn.trigger_event_sequence<>job.reviewed_event_sequence))
   OR (job.status='leased')<>(job.lease_expires_at IS NOT NULL)
   OR crewfold_timestamp_canonical(job.available_at)<>1 OR crewfold_timestamp_canonical(job.created_at)<>1 OR crewfold_timestamp_canonical(job.updated_at)<>1
UNION ALL
SELECT 'owner_operation:'||operation.id FROM owner_turn_operations operation
LEFT JOIN owner_effect_receipts receipt ON receipt.operation_id=operation.id
WHERE operation.payload_sha256<>lower(hex(sha256(CAST(operation.payload_json AS BLOB))))
   OR (operation.status='applied')<>(receipt.operation_id IS NOT NULL)
   OR (receipt.operation_id IS NOT NULL AND (receipt.response_sha256<>lower(hex(sha256(CAST(receipt.response_json AS BLOB))))
      OR receipt.event_sequence IS NOT operation.event_sequence
      OR NOT EXISTS(SELECT 1 FROM events event WHERE event.sequence=receipt.event_sequence)))`},
		{name: "scheduling_receipts", query: `
SELECT 'scheduling:'||receipt.run_id AS sample FROM run_scheduling_receipts receipt
JOIN runs run ON run.id=receipt.run_id JOIN scheduling_intents intent ON intent.id=receipt.intent_id
JOIN supervisor_actions action ON action.id=receipt.action_id JOIN task_assignments assignment ON assignment.id=receipt.assignment_id
WHERE run.workspace_id<>receipt.workspace_id OR run.assignment_id<>receipt.assignment_id OR intent.run_id<>run.id
   OR action.run_id<>run.id OR action.intent_id<>intent.id OR assignment.task_id<>run.task_id OR assignment.agent_id<>run.agent_id
UNION ALL
SELECT 'retry:'||receipt.run_id FROM run_retry_receipts receipt
JOIN runs run ON run.id=receipt.run_id JOIN runs prior ON prior.id=receipt.prior_run_id
JOIN supervisor_actions action ON action.id=receipt.action_id
WHERE run.workspace_id<>receipt.workspace_id OR prior.workspace_id<>receipt.workspace_id OR action.run_id<>run.id OR action.prior_run_id<>prior.id
UNION ALL
SELECT 'intent:'||intent.id FROM scheduling_intents intent
WHERE (intent.status='run_requested' AND (intent.run_id IS NULL OR intent.assignment_id IS NULL))
   OR (intent.status='satisfied' AND intent.run_id IS NULL)`},
	},
	"messaging": {
		{name: "delivery_and_wake_state", query: `
SELECT 'message_scope:'||message.id AS sample FROM messages message JOIN message_threads thread ON thread.id=message.thread_id
WHERE message.workspace_id<>thread.workspace_id OR (message.project_id IS NOT thread.project_id) OR (message.task_id IS NOT thread.task_id)
   OR (message.reply_to_message_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM messages reply WHERE reply.id=message.reply_to_message_id AND reply.thread_id=message.thread_id))
UNION ALL
SELECT 'sender:'||message.id FROM messages message LEFT JOIN runs run ON run.id=message.sender_run_id
WHERE (message.sender_type='agent_run' AND (run.id IS NULL OR message.sender_id<>run.id OR message.sender_agent_id<>run.agent_id OR message.workspace_id<>run.workspace_id))
   OR (message.sender_type<>'agent_run' AND (message.sender_agent_id IS NOT NULL OR message.sender_run_id IS NOT NULL))
UNION ALL
SELECT 'recipient:'||recipient.message_id||':'||recipient.recipient_agent_id FROM message_recipients recipient
WHERE (recipient.status='queued' AND (recipient.delivered_at IS NOT NULL OR recipient.read_at IS NOT NULL OR recipient.acknowledged_at IS NOT NULL))
   OR (recipient.status='delivered' AND (recipient.delivered_at IS NULL OR recipient.read_at IS NOT NULL OR recipient.acknowledged_at IS NOT NULL))
   OR (recipient.status='read' AND (recipient.delivered_at IS NULL OR recipient.read_at IS NULL OR recipient.acknowledged_at IS NOT NULL))
   OR (recipient.status='acknowledged' AND (recipient.delivered_at IS NULL OR recipient.read_at IS NULL OR recipient.acknowledged_at IS NULL))
UNION ALL
SELECT 'wake:'||wake.id FROM message_wake_jobs wake
LEFT JOIN message_recipients recipient ON recipient.message_id=wake.message_id AND recipient.recipient_agent_id=wake.recipient_agent_id
LEFT JOIN events event ON event.entity_type='message' AND event.entity_id=wake.message_id AND event.correlation_id='wake-unknown-'||wake.id
WHERE recipient.message_id IS NULL OR (wake.status='leased')<>(wake.lease_expires_at IS NOT NULL)
   OR (wake.status='succeeded' AND recipient.status='queued')
   OR (wake.status='failed_unknown' AND (event.type<>'message.wake_failed_unknown' OR event.actor_id<>'message-wake-worker' OR event.actor_type<>'subsystem'))`},
	},
	"checks": {
		{name: "lifecycle_job_result_receipts", query: `
SELECT 'missing_job:'||run.id AS sample FROM check_runs run LEFT JOIN check_jobs job ON job.check_run_id=run.id WHERE job.id IS NULL
UNION ALL
SELECT 'job_lease:'||job.id FROM check_jobs job WHERE (job.status='leased')<>(job.lease_expires_at IS NOT NULL)
UNION ALL
SELECT 'terminal:'||run.id FROM check_runs run JOIN check_jobs job ON job.check_run_id=run.id
LEFT JOIN check_results result ON result.check_run_id=run.id
WHERE run.status='finished' AND (job.status<>'complete' OR run.finished_at IS NULL OR result.id IS NULL)
UNION ALL
SELECT 'nonterminal:'||run.id FROM check_runs run WHERE run.status<>'finished' AND run.finished_at IS NOT NULL
UNION ALL
SELECT 'launch:'||receipt.id FROM check_launch_receipts receipt
JOIN check_runs run ON run.id=receipt.check_run_id JOIN check_jobs job ON job.id=receipt.check_job_id
WHERE job.check_run_id<>run.id OR receipt.definition_sha256<>run.definition_sha256 OR receipt.repository_id<>run.repository_id
   OR receipt.object_format<>run.repository_object_format OR receipt.checkout_id<>run.checkout_id
UNION ALL
SELECT 'freshness:'||result.id FROM check_results result
WHERE NOT EXISTS(SELECT 1 FROM check_result_freshness freshness WHERE freshness.check_result_id=result.id AND freshness.revision=1)
   OR NOT EXISTS(SELECT 1 FROM check_requirement_evidence evidence WHERE evidence.check_result_id=result.id AND evidence.freshness_revision=1)`},
		{name: "runtime_binding_parity", query: `
SELECT 'check_runtime_binding:'||run.id AS sample FROM check_runs run
LEFT JOIN check_runtime_bindings binding ON binding.check_run_id=run.id
WHERE (run.status='running' AND binding.check_run_id IS NULL)
   OR (binding.check_run_id IS NOT NULL AND (binding.operation_id<>run.id OR run.status NOT IN ('starting','running')))
   OR (binding.check_run_id IS NOT NULL AND (crewfold_timestamp_canonical(binding.created_at)<>1 OR crewfold_timestamp_canonical(binding.updated_at)<>1))`},
		{name: "content_hashes_and_children", query: `
SELECT 'definition:'||definition.id AS sample FROM check_definitions definition
WHERE definition.content_sha256<>lower(hex(sha256(CAST(definition.content_json AS BLOB))))
   OR json_array_length(definition.arguments_json)<>(SELECT count(*) FROM check_definition_arguments argument WHERE argument.definition_id=definition.id)
UNION ALL
SELECT 'grant:'||grant_row.id FROM check_watch_grants grant_row
WHERE grant_row.content_sha256<>lower(hex(sha256(CAST(grant_row.content_json AS BLOB))))
   OR json_array_length(grant_row.operations_json)<>(SELECT count(*) FROM check_watch_grant_operations operation WHERE operation.grant_id=grant_row.id)
   OR json_array_length(grant_row.definitions_json)<>(SELECT count(*) FROM check_watch_grant_definitions definition WHERE definition.grant_id=grant_row.id)
UNION ALL
SELECT 'artifact:'||artifact.id FROM check_artifacts artifact LEFT JOIN immutable_artifacts catalog ON catalog.content_sha256=artifact.content_sha256
WHERE catalog.content_sha256 IS NULL OR catalog.byte_size<>artifact.captured_bytes
UNION ALL
SELECT 'repair_decision:'||decision.repair_proposal_id FROM check_repair_decisions decision
JOIN check_repair_proposals proposal ON proposal.id=decision.repair_proposal_id
WHERE proposal.status<>decision.decision OR proposal.revision<>decision.proposal_revision+1
UNION ALL
SELECT 'repair_effect:'||effect.repair_proposal_id FROM check_repair_effects effect
JOIN check_repair_proposals proposal ON proposal.id=effect.repair_proposal_id
WHERE proposal.status<>'accepted'`},
	},
	"outcomes": {
		{name: "receipts_governance_and_child_counts", query: `
SELECT 'commitment:'||commitment.id AS sample FROM deliverable_commitments commitment
LEFT JOIN outcome_commitment_receipts receipt ON receipt.commitment_id=commitment.id
LEFT JOIN events event ON event.sequence=receipt.event_sequence
WHERE commitment.content_sha256<>lower(hex(sha256(CAST(commitment.content_json AS BLOB))))
   OR receipt.commitment_id IS NULL OR event.type<>'outcome.commitment_created' OR event.entity_id<>commitment.id
UNION ALL
SELECT 'assessment:'||assessment.id FROM outcome_assessments assessment
LEFT JOIN outcome_assessment_submissions submission ON submission.assessment_id=assessment.id
WHERE assessment.content_sha256<>lower(hex(sha256(CAST(assessment.content_json AS BLOB)))) OR submission.assessment_id IS NULL
   OR submission.child_count<>(SELECT count(*) FROM outcome_assessment_decision_refs child WHERE child.assessment_id=assessment.id)
      +(SELECT count(*) FROM outcome_assessment_evidence_refs child WHERE child.assessment_id=assessment.id)
      +(SELECT count(*) FROM outcome_assessment_effects child WHERE child.assessment_id=assessment.id)
      +(SELECT count(*) FROM outcome_assessment_deviations child WHERE child.assessment_id=assessment.id)
      +(SELECT count(*) FROM outcome_assessment_risks child WHERE child.assessment_id=assessment.id)
      +(SELECT count(*) FROM outcome_assessment_unknowns child WHERE child.assessment_id=assessment.id)
      +(SELECT count(*) FROM outcome_assessment_follow_up_tasks child WHERE child.assessment_id=assessment.id)
      +(SELECT count(*) FROM outcome_assessment_owner_attention child WHERE child.assessment_id=assessment.id)
UNION ALL
SELECT 'governance:'||governance.assessment_id FROM outcome_assessment_governance governance
JOIN outcome_assessments assessment ON assessment.id=governance.assessment_id LEFT JOIN events event ON event.sequence=governance.decision_event_sequence
WHERE NOT ((governance.decision='accepted' AND assessment.review_state IN ('accepted','superseded'))
        OR (governance.decision='rejected' AND assessment.review_state='rejected'))
   OR assessment.decided_at<>governance.decided_at
   OR event.entity_id<>assessment.id
   OR (assessment.review_state<>'superseded' AND event.entity_revision<>assessment.state_revision)
   OR (assessment.review_state='superseded' AND event.entity_revision>=assessment.state_revision)
   OR event.type<>CASE governance.decision WHEN 'accepted' THEN 'outcome.assessment_accepted' ELSE 'outcome.assessment_rejected' END
   OR (governance.decision='accepted' AND NOT EXISTS(SELECT 1 FROM outcome_assessment_acceptance_basis basis WHERE basis.assessment_id=assessment.id))
UNION ALL
SELECT 'checkpoint:'||checkpoint.id FROM owner_checkpoints checkpoint LEFT JOIN events event ON event.sequence=checkpoint.event_sequence
WHERE event.type<>'owner_checkpoint.created' OR event.entity_type<>'owner_checkpoint' OR event.entity_id<>checkpoint.id
UNION ALL
SELECT 'projector:'||state.workspace_id FROM outcome_projector_state state
WHERE state.last_event_sequence>(SELECT COALESCE(max(sequence),0) FROM events)`},
		{name: "briefing_seal", query: `
SELECT 'briefing:'||briefing.id AS sample FROM management_briefings briefing
LEFT JOIN management_briefing_receipts receipt ON receipt.briefing_id=briefing.id
WHERE briefing.content_sha256<>lower(hex(sha256(CAST(briefing.content_json AS BLOB))))
   OR briefing.byte_size<>length(CAST(briefing.content_json AS BLOB)) OR briefing.created_at<>briefing.evaluated_at
   OR receipt.briefing_id IS NULL OR receipt.sealed_at<>briefing.created_at
   OR receipt.claim_count<>(SELECT count(*) FROM management_briefing_claims claim WHERE claim.briefing_id=briefing.id)
   OR receipt.source_count<>(SELECT count(*) FROM management_briefing_claim_sources source WHERE source.briefing_id=briefing.id)
UNION ALL
SELECT 'briefing_claims:'||claim.briefing_id FROM management_briefing_claims claim
GROUP BY claim.briefing_id HAVING min(claim.ordinal)<>0 OR max(claim.ordinal)+1<>count(*)
UNION ALL
SELECT 'briefing_sources:'||source.briefing_id||':'||source.claim_id FROM management_briefing_claim_sources source
GROUP BY source.briefing_id,source.claim_id HAVING min(source.ordinal)<>0 OR max(source.ordinal)+1<>count(*)`},
	},
}

func validateSemanticFamily(ctx context.Context, tx *sql.Tx, family, runtimeNodeID, runtimeNodeFingerprint string) ([]SemanticIntegrityViolation, error) {
	checks, ok := semanticFamilyChecks[family]
	if !ok || len(checks) == 0 {
		return nil, fmt.Errorf("semantic family %q has no executable validators", family)
	}
	violations := make([]SemanticIntegrityViolation, 0)
	if family == "core" {
		violation, err := validateCanonicalEvents(ctx, tx)
		if err != nil {
			return nil, err
		}
		if violation.Count != 0 {
			violations = append(violations, violation)
		}
	}
	if family == "run" || family == "checks" {
		violation, err := validateCurrentNodeRuntimeBindings(ctx, tx, family, runtimeNodeID, runtimeNodeFingerprint)
		if err != nil {
			return nil, err
		}
		if violation.Count != 0 {
			violations = append(violations, violation)
		}
	}
	for _, check := range checks {
		violation, err := runSemanticSQLCheck(ctx, tx, check)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", check.name, err)
		}
		if violation.Count != 0 {
			violations = append(violations, violation)
		}
	}
	return violations, nil
}

func validateCurrentNodeRuntimeBindings(ctx context.Context, tx *sql.Tx, family, nodeID, nodeFingerprint string) (SemanticIntegrityViolation, error) {
	violation := SemanticIntegrityViolation{Check: "current_node_runtime_binding", Samples: []string{}}
	// Offline snapshot/recovery verification intentionally has no node key. It
	// still audits binding structure and quiescence, but only a live Store with a
	// canonical node identity can decide whether a binding belongs to itself.
	if !validRuntimeNodeIdentity(nodeID, nodeFingerprint) {
		return violation, nil
	}
	table, idColumn, samplePrefix := "run_runtime_bindings", "run_id", "run_runtime_binding:"
	if family == "checks" {
		table, idColumn, samplePrefix = "check_runtime_bindings", "check_run_id", "check_runtime_binding:"
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
SELECT %s FROM %s
WHERE node_id<>? OR node_fingerprint<>?
ORDER BY %s`, quoteSQLiteIdentifier(idColumn), quoteSQLiteIdentifier(table), quoteSQLiteIdentifier(idColumn)), nodeID, nodeFingerprint)
	if err != nil {
		return SemanticIntegrityViolation{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return SemanticIntegrityViolation{}, err
		}
		violation.Count++
		if len(violation.Samples) < maximumSemanticViolationSamples {
			violation.Samples = append(violation.Samples, samplePrefix+id)
		}
	}
	if err := rows.Err(); err != nil {
		return SemanticIntegrityViolation{}, err
	}
	return violation, nil
}

func validateKnowledgeFTSReadOnly(ctx context.Context, tx *sql.Tx) (string, string, error) {
	if diagnosis := knowledgeIndexSchemaDiagnosis(ctx, tx); diagnosis != "" {
		return domain.KnowledgeIndexDegraded, diagnosis, nil
	}
	var builtAt, sourceDigest string
	var sourceEventSequence, sourceCount int64
	if err := tx.QueryRowContext(ctx, `SELECT built_at,source_event_sequence,source_count,source_digest
FROM knowledge_search_metadata WHERE singleton=1`).Scan(&builtAt, &sourceEventSequence, &sourceCount, &sourceDigest); err != nil {
		return domain.KnowledgeIndexDegraded, domain.KnowledgeIndexCorrupt, nil
	}
	if !canonicalTimestampText(builtAt) {
		return domain.KnowledgeIndexDegraded, domain.KnowledgeIndexCorrupt, nil
	}
	source, err := knowledgeIndexCanonicalSource(ctx, tx)
	if err != nil {
		return "", "", err
	}
	if sourceEventSequence > source.EventSequence {
		return domain.KnowledgeIndexDegraded, domain.KnowledgeIndexCorrupt, nil
	}
	if sourceCount != source.Count || sourceDigest != source.Digest {
		return domain.KnowledgeIndexDegraded, domain.KnowledgeIndexOutOfDate, nil
	}
	mismatch, err := knowledgeSearchContentMismatch(ctx, tx, source.Count)
	if err != nil {
		return "", "", err
	}
	if mismatch {
		return domain.KnowledgeIndexDegraded, domain.KnowledgeIndexContentMismatch, nil
	}
	return domain.KnowledgeIndexOK, "", nil
}

func runSemanticSQLCheck(ctx context.Context, tx *sql.Tx, check semanticSQLCheck) (SemanticIntegrityViolation, error) {
	rows, err := tx.QueryContext(ctx, "SELECT sample FROM ("+check.query+") ORDER BY sample")
	if err != nil {
		return SemanticIntegrityViolation{}, err
	}
	defer rows.Close()
	violation := SemanticIntegrityViolation{Check: check.name, Samples: []string{}}
	for rows.Next() {
		var sample string
		if err := rows.Scan(&sample); err != nil {
			return SemanticIntegrityViolation{}, err
		}
		violation.Count++
		if len(violation.Samples) < maximumSemanticViolationSamples {
			violation.Samples = append(violation.Samples, sample)
		}
	}
	if err := rows.Err(); err != nil {
		return SemanticIntegrityViolation{}, err
	}
	return violation, nil
}

func validateCanonicalEvents(ctx context.Context, tx *sql.Tx) (SemanticIntegrityViolation, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT sequence,event_id,type,schema_version,occurred_at,recorded_at,actor_id,actor_type,
 workspace_id,entity_type,entity_id,entity_revision,correlation_id,causation_id,data_json
FROM events ORDER BY sequence`)
	if err != nil {
		return SemanticIntegrityViolation{}, err
	}
	defer rows.Close()
	violation := SemanticIntegrityViolation{Check: "known_canonical_event_envelope", Samples: []string{}}
	expectedSequence := int64(1)
	for rows.Next() {
		var event domain.Event
		var causation sql.NullString
		var data string
		if err := rows.Scan(&event.Sequence, &event.EventID, &event.Type, &event.SchemaVersion,
			&event.OccurredAt, &event.RecordedAt, &event.Actor.ActorID, &event.Actor.ActorType,
			&event.WorkspaceID, &event.Entity.Type, &event.Entity.ID, &event.Entity.Revision,
			&event.CorrelationID, &causation, &data); err != nil {
			return SemanticIntegrityViolation{}, err
		}
		if causation.Valid {
			event.CausationID = causation.String
		}
		event.Data = json.RawMessage(data)
		valid := (!causation.Valid || causation.String != "") && event.Sequence == expectedSequence && event.SchemaVersion == 1 && domain.KnownEventType(event.Type) &&
			domain.ValidEvent(event) && canonicalTimestampText(event.OccurredAt) && canonicalTimestampText(event.RecordedAt) && canonicalJSONObject([]byte(data))
		if !valid {
			violation.Count++
			if len(violation.Samples) < maximumSemanticViolationSamples {
				violation.Samples = append(violation.Samples, fmt.Sprintf("sequence:%d type:%s", event.Sequence, event.Type))
			}
		}
		expectedSequence = event.Sequence + 1
	}
	if err := rows.Err(); err != nil {
		return SemanticIntegrityViolation{}, err
	}
	return violation, nil
}

func canonicalTimestampText(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC && parsed.Format(time.RFC3339Nano) == value
}

func canonicalJSONObject(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return false
	}
	canonical, err := json.Marshal(value)
	return err == nil && bytes.Equal(canonical, data)
}

func semanticValidatorFamilyNames() []string {
	result := make([]string, 0, len(semanticFamilyChecks))
	for family := range semanticFamilyChecks {
		result = append(result, family)
	}
	sort.Strings(result)
	return result
}
