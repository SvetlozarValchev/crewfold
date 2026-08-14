-- name: InsertCheckDefinitionArgument :exec
INSERT INTO check_definition_arguments(definition_id,ordinal,argument)
VALUES(sqlc.arg(definition_id),sqlc.arg(ordinal),sqlc.arg(argument));

-- name: InsertCheckDefinition :exec
INSERT INTO check_definitions(id,workspace_id,project_id,name,executable,working_directory,timeout_millis,output_byte_limit,arguments_json,content_json,content_revision,content_sha256,status,revision,created_at,updated_at,created_by,updated_by)
VALUES(sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),sqlc.arg(name),sqlc.arg(executable),sqlc.arg(working_directory),sqlc.arg(timeout_millis),sqlc.arg(output_byte_limit),sqlc.arg(arguments_json),sqlc.arg(content_json),1,sqlc.arg(content_sha256),'active',1,sqlc.arg(created_at),sqlc.arg(created_at),'local-owner','local-owner');

-- name: GetCheckDefinitionByID :one
SELECT id,workspace_id,project_id,name,executable,working_directory,timeout_millis,output_byte_limit,content_revision,content_sha256,status,revision,created_at,updated_at,created_by,updated_by
FROM check_definitions WHERE workspace_id=sqlc.arg(workspace_id) AND id=sqlc.arg(identifier);

-- name: GetActiveCheckDefinitionByName :one
SELECT id,workspace_id,project_id,name,executable,working_directory,timeout_millis,output_byte_limit,content_revision,content_sha256,status,revision,created_at,updated_at,created_by,updated_by
FROM check_definitions WHERE workspace_id=sqlc.arg(workspace_id) AND name=sqlc.arg(identifier) AND status='active';

-- name: ListCheckDefinitionArguments :many
SELECT argument FROM check_definition_arguments WHERE definition_id=sqlc.arg(definition_id) ORDER BY ordinal;

-- name: ListCheckDefinitionIDs :many
SELECT id FROM check_definitions WHERE workspace_id=sqlc.arg(workspace_id)
 AND (CAST(sqlc.arg(project_id) AS TEXT)='' OR project_id=sqlc.arg(project_id))
 AND (CAST(sqlc.arg(status) AS TEXT)='' OR status=sqlc.arg(status))
 ORDER BY project_id,name,id LIMIT sqlc.arg(result_limit);

-- name: RetireCheckDefinition :execrows
UPDATE check_definitions SET status='retired',revision=revision+1,updated_at=sqlc.arg(updated_at),updated_by='local-owner'
WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND status='active' AND revision=sqlc.arg(expected_revision);

-- name: InsertTaskCheckRequirement :exec
INSERT INTO task_check_requirements(id,workspace_id,project_id,task_id,task_revision_at_creation,criterion_key,statement,definition_id,definition_content_revision,status,revision,created_at,updated_at,created_by,updated_by)
VALUES(sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),sqlc.arg(task_id),sqlc.arg(task_revision),sqlc.arg(criterion_key),sqlc.arg(statement),sqlc.arg(definition_id),sqlc.arg(definition_content_revision),'active',1,sqlc.arg(created_at),sqlc.arg(created_at),'local-owner','local-owner');

-- name: GetTaskCheckRequirement :one
SELECT id,workspace_id,project_id,task_id,task_revision_at_creation,criterion_key,statement,definition_id,definition_content_revision,status,revision,created_at,updated_at,created_by,updated_by
FROM task_check_requirements WHERE workspace_id=sqlc.arg(workspace_id) AND id=sqlc.arg(id);

-- name: GetActiveTaskCheckRequirementByDefinition :one
SELECT id,workspace_id,project_id,task_id,task_revision_at_creation,criterion_key,statement,definition_id,definition_content_revision,status,revision,created_at,updated_at,created_by,updated_by
FROM task_check_requirements WHERE workspace_id=sqlc.arg(workspace_id) AND task_id=sqlc.arg(task_id) AND definition_id=sqlc.arg(definition_id) AND status='active' LIMIT 1;

-- name: ListTaskCheckRequirementIDs :many
SELECT id FROM task_check_requirements WHERE workspace_id=sqlc.arg(workspace_id)
 AND (CAST(sqlc.arg(project_id) AS TEXT)='' OR project_id=sqlc.arg(project_id))
 AND (CAST(sqlc.arg(task_id) AS TEXT)='' OR task_id=sqlc.arg(task_id))
 AND (CAST(sqlc.arg(status) AS TEXT)='' OR status=sqlc.arg(status))
 ORDER BY project_id,task_id,criterion_key,id LIMIT sqlc.arg(result_limit);

-- name: RetireTaskCheckRequirement :execrows
UPDATE task_check_requirements SET status='retired',revision=revision+1,updated_at=sqlc.arg(updated_at),updated_by='local-owner'
WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND status='active' AND revision=sqlc.arg(expected_revision);

-- name: InsertCheckWatchGrantOperation :exec
INSERT INTO check_watch_grant_operations(grant_id,ordinal,operation) VALUES(sqlc.arg(grant_id),sqlc.arg(ordinal),sqlc.arg(operation));

-- name: InsertCheckWatchGrantDefinition :exec
INSERT INTO check_watch_grant_definitions(grant_id,ordinal,definition_id,definition_content_revision,definition_sha256)
VALUES(sqlc.arg(grant_id),sqlc.arg(ordinal),sqlc.arg(definition_id),sqlc.arg(content_revision),sqlc.arg(definition_sha256));

-- name: InsertCheckWatchGrant :exec
INSERT INTO check_watch_grants(id,workspace_id,project_id,agent_id,agent_revision,operations_json,definitions_json,max_pending,max_in_flight,expires_at,content_json,content_sha256,status,revision,created_at,updated_at,created_by,updated_by)
VALUES(sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),sqlc.arg(agent_id),sqlc.arg(agent_revision),sqlc.arg(operations_json),sqlc.arg(definitions_json),sqlc.arg(max_pending),sqlc.arg(max_in_flight),NULLIF(sqlc.arg(expires_at),''),sqlc.arg(content_json),sqlc.arg(content_sha256),'active',1,sqlc.arg(created_at),sqlc.arg(created_at),'local-owner','local-owner');

-- name: GetCheckWatchGrant :one
SELECT id,workspace_id,project_id,agent_id,agent_revision,max_pending,max_in_flight,expires_at,content_sha256,status,revision,created_at,updated_at,created_by,updated_by
FROM check_watch_grants WHERE workspace_id=sqlc.arg(workspace_id) AND id=sqlc.arg(id);

-- name: ListCheckWatchGrantOperations :many
SELECT operation FROM check_watch_grant_operations WHERE grant_id=sqlc.arg(grant_id) ORDER BY ordinal;

-- name: ListCheckWatchGrantDefinitions :many
SELECT definition_id,definition_content_revision,definition_sha256 FROM check_watch_grant_definitions WHERE grant_id=sqlc.arg(grant_id) ORDER BY ordinal;

-- name: ListCheckWatchGrantIDs :many
SELECT id FROM check_watch_grants WHERE workspace_id=sqlc.arg(workspace_id)
 AND (CAST(sqlc.arg(project_id) AS TEXT)='' OR project_id=sqlc.arg(project_id))
 AND (CAST(sqlc.arg(agent_id) AS TEXT)='' OR agent_id=sqlc.arg(agent_id))
 AND (CAST(sqlc.arg(status) AS TEXT)='' OR status=sqlc.arg(status))
 ORDER BY project_id,created_at,id LIMIT sqlc.arg(result_limit);

-- name: RevokeCheckWatchGrant :execrows
UPDATE check_watch_grants SET status='revoked',revision=revision+1,updated_at=sqlc.arg(updated_at),updated_by='local-owner'
WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND status='active' AND revision=sqlc.arg(expected_revision);

-- name: GetCheckPolicy :one
SELECT workspace_id,project_id,repair_proposals_enabled,repair_launch_profile_id,repair_launch_profile_revision,max_open_repair_proposals,revision,created_at,updated_at,created_by,updated_by
FROM check_policies WHERE workspace_id=sqlc.arg(workspace_id) AND project_id=sqlc.arg(project_id);

-- name: UpdateCheckPolicy :execrows
UPDATE check_policies SET repair_proposals_enabled=sqlc.arg(enabled),repair_launch_profile_id=NULLIF(sqlc.arg(profile_id),''),repair_launch_profile_revision=NULLIF(sqlc.arg(profile_revision),0),max_open_repair_proposals=sqlc.arg(max_open),revision=revision+1,updated_at=sqlc.arg(updated_at),updated_by='local-owner'
WHERE workspace_id=sqlc.arg(workspace_id) AND project_id=sqlc.arg(project_id) AND revision=sqlc.arg(expected_revision);

-- name: InsertCheckRoute :exec
INSERT INTO check_routes(id,workspace_id,project_id,definition_id,definition_content_revision,trigger,duty,agent_id,agent_revision,status,revision,created_at,updated_at,created_by,updated_by)
VALUES(sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),NULLIF(sqlc.arg(definition_id),''),NULLIF(sqlc.arg(definition_content_revision),0),sqlc.arg(trigger),sqlc.arg(duty),sqlc.arg(agent_id),sqlc.arg(agent_revision),'active',1,sqlc.arg(created_at),sqlc.arg(created_at),'local-owner','local-owner');

-- name: GetCheckRoute :one
SELECT id,workspace_id,project_id,definition_id,definition_content_revision,trigger,duty,agent_id,agent_revision,status,revision,created_at,updated_at,created_by,updated_by FROM check_routes WHERE workspace_id=sqlc.arg(workspace_id) AND id=sqlc.arg(id);

-- name: ListCheckRouteIDs :many
SELECT id FROM check_routes WHERE workspace_id=sqlc.arg(workspace_id) AND (CAST(sqlc.arg(project_id) AS TEXT)='' OR project_id=sqlc.arg(project_id)) AND (CAST(sqlc.arg(definition_id) AS TEXT)='' OR definition_id=sqlc.arg(definition_id)) AND (CAST(sqlc.arg(trigger) AS TEXT)='' OR trigger=sqlc.arg(trigger)) AND (CAST(sqlc.arg(duty) AS TEXT)='' OR duty=sqlc.arg(duty)) AND (CAST(sqlc.arg(status) AS TEXT)='' OR status=sqlc.arg(status)) ORDER BY project_id,created_at,id LIMIT sqlc.arg(result_limit);

-- name: RetireCheckRoute :execrows
UPDATE check_routes SET status='retired',revision=revision+1,updated_at=sqlc.arg(updated_at),updated_by='local-owner' WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND status='active' AND revision=sqlc.arg(expected_revision);

-- name: InsertCheckRun :exec
INSERT INTO check_runs(id,workspace_id,project_id,task_id,task_revision,requirement_id,requirement_revision,definition_id,definition_content_revision,definition_sha256,checkout_id,checkout_revision,repository_id,repository_object_format,checkout_path,checkout_write_mode,source_type,source_actor_id,source_agent_id,source_agent_revision,source_run_id,source_grant_id,source_grant_revision,source_max_in_flight,status,runtime_handle,revision,created_at,updated_at,started_at,finished_at,created_by,updated_by)
VALUES(sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),sqlc.arg(task_id),sqlc.arg(task_revision),sqlc.arg(requirement_id),sqlc.arg(requirement_revision),sqlc.arg(definition_id),sqlc.arg(definition_content_revision),sqlc.arg(definition_sha256),sqlc.arg(checkout_id),sqlc.arg(checkout_revision),sqlc.arg(repository_id),sqlc.arg(repository_object_format),sqlc.arg(checkout_path),sqlc.arg(checkout_write_mode),sqlc.arg(source_type),sqlc.arg(source_actor_id),NULLIF(sqlc.arg(source_agent_id),''),NULLIF(sqlc.arg(source_agent_revision),0),NULLIF(sqlc.arg(source_run_id),''),NULLIF(sqlc.arg(source_grant_id),''),NULLIF(sqlc.arg(source_grant_revision),0),sqlc.arg(source_max_in_flight),'requested',NULL,1,sqlc.arg(created_at),sqlc.arg(created_at),NULL,NULL,sqlc.arg(created_by),sqlc.arg(created_by));

-- name: InsertCheckJob :exec
INSERT INTO check_jobs(id,check_run_id,status,available_at,lease_expires_at,attempts,created_at,updated_at) VALUES(sqlc.arg(id),sqlc.arg(check_run_id),'pending',sqlc.arg(created_at),NULL,0,sqlc.arg(created_at),sqlc.arg(created_at));

-- name: GetCheckRun :one
SELECT * FROM check_runs WHERE id=sqlc.arg(id);

-- name: GetCheckJobByRun :one
SELECT * FROM check_jobs WHERE check_run_id=sqlc.arg(check_run_id);

-- name: GetCheckLaunchReceiptByRun :one
SELECT * FROM check_launch_receipts WHERE check_run_id=sqlc.arg(check_run_id);

-- name: ListCheckWatchScopes :many
SELECT project.workspace_id, workspace.name AS workspace_name, project.id AS project_id, project.name AS project_name
FROM projects project
JOIN workspaces workspace ON workspace.id=project.workspace_id
WHERE project.id>sqlc.arg(after_project_id)
ORDER BY project.id
LIMIT sqlc.arg(result_limit);

-- name: GetCheckWatchScope :one
SELECT workspace.id AS workspace_id, workspace.name AS workspace_name, project.id AS project_id, project.name AS project_name
FROM projects project
JOIN workspaces workspace ON workspace.id=project.workspace_id
WHERE (workspace.id=sqlc.arg(workspace_identifier) OR workspace.name=sqlc.arg(workspace_identifier))
  AND (project.id=sqlc.arg(project_identifier) OR project.name=sqlc.arg(project_identifier));

-- name: GetCheckWatchState :one
SELECT last_event_sequence,last_result_id,revision,created_at,updated_at
FROM check_watch_state
WHERE workspace_id=sqlc.arg(workspace_id) AND project_id=sqlc.arg(project_id);

-- name: GetCheckWatchEventCutoff :one
SELECT CAST(COALESCE(MAX(sequence),0) AS INTEGER) AS cutoff_sequence FROM events WHERE workspace_id=sqlc.arg(workspace_id);

-- name: ListCheckWatchJournalEvents :many
SELECT sequence,type FROM events
WHERE workspace_id=sqlc.arg(workspace_id) AND sequence>sqlc.arg(after_sequence) AND sequence<=sqlc.arg(cutoff_sequence)
ORDER BY sequence LIMIT sqlc.arg(result_limit);

-- name: ListCheckWatchCandidates :many
SELECT result.id AS check_result_id, freshness.revision AS freshness_revision,
 repository.id AS repository_id, repository.revision AS repository_revision,
 repository.fingerprint AS repository_fingerprint, repository.object_format,
 checkout.id AS checkout_id, checkout.revision AS checkout_revision, checkout.path AS checkout_path
FROM check_results result
JOIN check_runs run ON run.id=result.check_run_id
JOIN task_check_requirements requirement ON requirement.id=result.requirement_id
JOIN repositories repository ON repository.id=run.repository_id
JOIN checkouts checkout ON checkout.id=run.checkout_id
JOIN check_result_freshness freshness ON freshness.check_result_id=result.id
 AND freshness.revision=(SELECT MAX(current.revision) FROM check_result_freshness current WHERE current.check_result_id=result.id)
WHERE run.workspace_id=sqlc.arg(workspace_id) AND run.project_id=sqlc.arg(project_id)
 AND requirement.status='active' AND requirement.revision=result.requirement_revision AND requirement.task_id=run.task_id
 AND result.id>sqlc.arg(after_result_id)
 AND NOT EXISTS(SELECT 1 FROM check_results newer JOIN check_runs newer_run ON newer_run.id=newer.check_run_id
   WHERE newer.requirement_id=result.requirement_id AND newer.requirement_revision=result.requirement_revision
     AND (newer.created_at>result.created_at OR (newer.created_at=result.created_at AND newer.id>result.id)))
ORDER BY result.id LIMIT sqlc.arg(result_limit);

-- name: GetCheckWatchCandidateState :one
SELECT result.id AS check_result_id,result.check_run_id,result.requirement_id,result.requirement_revision,result.outcome,
 result.head_commit AS baseline_head_commit,freshness.revision AS freshness_revision,
 freshness.status AS freshness_status,freshness.initially_eligible,freshness.ever_stale,
 repository.id AS repository_id,repository.revision AS repository_revision,
 repository.fingerprint AS repository_fingerprint,repository.object_format,
 checkout.id AS checkout_id,checkout.revision AS checkout_revision,checkout.path AS checkout_path
FROM check_results result
JOIN check_runs run ON run.id=result.check_run_id
JOIN task_check_requirements requirement ON requirement.id=result.requirement_id
JOIN repositories repository ON repository.id=run.repository_id
JOIN checkouts checkout ON checkout.id=run.checkout_id
JOIN check_result_freshness freshness ON freshness.check_result_id=result.id
 AND freshness.revision=(SELECT MAX(current.revision) FROM check_result_freshness current WHERE current.check_result_id=result.id)
WHERE result.id=sqlc.arg(check_result_id) AND run.workspace_id=sqlc.arg(workspace_id) AND run.project_id=sqlc.arg(project_id)
 AND requirement.status='active' AND requirement.revision=result.requirement_revision AND requirement.task_id=run.task_id
 AND NOT EXISTS(SELECT 1 FROM check_results newer JOIN check_runs newer_run ON newer_run.id=newer.check_run_id
   WHERE newer.requirement_id=result.requirement_id AND newer.requirement_revision=result.requirement_revision
     AND (newer.created_at>result.created_at OR (newer.created_at=result.created_at AND newer.id>result.id)));

-- name: AdvanceCheckWatchState :execrows
UPDATE check_watch_state
SET last_event_sequence=sqlc.arg(through_event_sequence),last_result_id=sqlc.arg(through_result_id),revision=revision+1,updated_at=sqlc.arg(updated_at)
WHERE workspace_id=sqlc.arg(workspace_id) AND project_id=sqlc.arg(project_id)
 AND last_event_sequence=sqlc.arg(from_event_sequence) AND last_result_id=sqlc.arg(from_result_id);

-- name: InsertObservedCheckFreshness :exec
INSERT INTO check_result_freshness(id,check_result_id,revision,status,reason,initially_eligible,ever_stale,observation_available,repository_id,object_format,checkout_id,branch,head_commit,dirty,dirty_paths_json,observed_at,diagnostic_code,diagnostic,created_at,created_by)
VALUES(sqlc.arg(id),sqlc.arg(check_result_id),sqlc.arg(revision),sqlc.arg(status),sqlc.arg(reason),sqlc.arg(initially_eligible),sqlc.arg(ever_stale),sqlc.arg(observation_available),sqlc.arg(repository_id),sqlc.arg(object_format),sqlc.arg(checkout_id),NULLIF(sqlc.arg(branch),''),NULLIF(sqlc.arg(head_commit),''),sqlc.arg(dirty),sqlc.arg(dirty_paths_json),sqlc.arg(observed_at),NULLIF(sqlc.arg(diagnostic_code),''),NULLIF(sqlc.arg(diagnostic),''),sqlc.arg(created_at),'crewfold-check-worker');

-- name: InsertObservedCheckEvidence :exec
INSERT INTO check_requirement_evidence(id,requirement_id,requirement_revision,check_result_id,freshness_revision,class,effect,created_at,created_by)
VALUES(sqlc.arg(id),sqlc.arg(requirement_id),sqlc.arg(requirement_revision),sqlc.arg(check_result_id),sqlc.arg(freshness_revision),'mechanical_check',sqlc.arg(effect),sqlc.arg(created_at),'crewfold-check-worker');

-- name: InsertCheckWatchReceipt :exec
INSERT INTO check_watch_receipts(id,workspace_id,project_id,from_event_sequence,through_event_sequence,cutoff_event_sequence,caught_up,degraded,unknown_event_type,unknown_event_sequence,examined_result_ids_json,freshness_appended,notifications_created,route_failures_created,repairs_marked_stale,next_cursor,content_json,content_sha256,created_at,created_by)
VALUES(sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),sqlc.arg(from_event_sequence),sqlc.arg(through_event_sequence),sqlc.arg(cutoff_event_sequence),sqlc.arg(caught_up),0,NULL,NULL,sqlc.arg(examined_result_ids_json),sqlc.arg(freshness_appended),sqlc.arg(notifications_created),sqlc.arg(route_failures_created),sqlc.arg(repairs_marked_stale),NULLIF(sqlc.arg(next_cursor),''),sqlc.arg(content_json),sqlc.arg(content_sha256),sqlc.arg(created_at),sqlc.arg(created_by));

-- name: GetCurrentCheckTaskOwner :one
SELECT assignment.id AS assignment_id,assignment.revision AS assignment_revision,agent.id AS agent_id,agent.revision AS agent_revision
FROM task_assignments assignment JOIN agents agent ON agent.id=assignment.agent_id
WHERE assignment.task_id=sqlc.arg(task_id) AND assignment.status='active'
 AND (crewfold_timestamp_key(assignment.lease_expires_at)>crewfold_timestamp_key(sqlc.arg(observed_at))
   OR EXISTS(SELECT 1 FROM runs reserved WHERE reserved.assignment_id=assignment.id AND reserved.task_id=assignment.task_id
     AND reserved.agent_id=assignment.agent_id AND reserved.status IN ('requested','starting','active','blocked','stopping','lost')))
 AND agent.enabled=1
LIMIT 1;

-- name: ListApplicableCheckRoutes :many
SELECT route.id,route.duty,route.agent_id,route.agent_revision,
 CASE WHEN agent.id IS NOT NULL AND agent.enabled=1 AND agent.revision=route.agent_revision THEN 1 ELSE 0 END AS recipient_available
FROM check_routes route LEFT JOIN agents agent ON agent.id=route.agent_id
WHERE route.workspace_id=sqlc.arg(workspace_id) AND route.project_id=sqlc.arg(project_id) AND route.status='active'
 AND (route.definition_id IS NULL OR (route.definition_id=sqlc.arg(definition_id) AND route.definition_content_revision=sqlc.arg(definition_content_revision)))
 AND (route.trigger=sqlc.arg(trigger) OR (sqlc.arg(trigger)='stale' AND route.trigger='stale'))
ORDER BY route.id;

-- name: InsertCheckNotificationReceipt :exec
INSERT INTO check_notification_receipts(id,workspace_id,project_id,task_id,check_result_id,freshness_revision,route_id,duty,recipient_agent_id,recipient_agent_revision,assignment_id,assignment_revision,message_id,created_at)
VALUES(sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),sqlc.arg(task_id),sqlc.arg(check_result_id),sqlc.arg(freshness_revision),NULLIF(sqlc.arg(route_id),''),sqlc.arg(duty),sqlc.arg(recipient_agent_id),sqlc.arg(recipient_agent_revision),NULLIF(sqlc.arg(assignment_id),''),NULLIF(sqlc.arg(assignment_revision),0),sqlc.arg(message_id),sqlc.arg(created_at));

-- name: InsertCheckRouteFailure :exec
INSERT INTO check_route_failures(id,workspace_id,project_id,task_id,check_result_id,freshness_revision,route_id,duty,recipient_agent_id,recipient_agent_revision,assignment_id,assignment_revision,code,diagnostic,created_at)
VALUES(sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),sqlc.arg(task_id),sqlc.arg(check_result_id),sqlc.arg(freshness_revision),NULLIF(sqlc.arg(route_id),''),sqlc.arg(duty),NULLIF(sqlc.arg(recipient_agent_id),''),NULLIF(sqlc.arg(recipient_agent_revision),0),NULLIF(sqlc.arg(assignment_id),''),NULLIF(sqlc.arg(assignment_revision),0),sqlc.arg(code),sqlc.arg(diagnostic),sqlc.arg(created_at));

-- name: InsertCheckNotificationThread :exec
INSERT INTO message_threads(id,workspace_id,project_id,task_id,subject,status,revision,created_at,updated_at,created_by,updated_by,kind,participant_revision,initial_participant_count)
VALUES(sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),sqlc.arg(task_id),sqlc.arg(subject),'open',1,sqlc.arg(created_at),sqlc.arg(created_at),'crewfold-check-worker','crewfold-check-worker','direct',0,0);

-- name: InsertCheckNotificationMessage :exec
INSERT INTO messages(id,workspace_id,thread_id,project_id,task_id,sender_type,sender_id,sender_agent_id,sender_run_id,kind,body,artifact_ids_json,reply_to_message_id,created_at)
VALUES(sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(thread_id),sqlc.arg(project_id),sqlc.arg(task_id),'subsystem','crewfold-check-worker',NULL,NULL,'inform',sqlc.arg(body),'[]',NULL,sqlc.arg(created_at));

-- name: InsertCheckNotificationRecipient :exec
INSERT INTO message_recipients(message_id,recipient_agent_id,status,queued_at,recipient_participant_id)
VALUES(sqlc.arg(message_id),sqlc.arg(recipient_agent_id),'queued',sqlc.arg(queued_at),NULL);

-- name: GetCheckNotificationWakeTarget :one
SELECT id FROM runs WHERE workspace_id=sqlc.arg(workspace_id) AND agent_id=sqlc.arg(agent_id)
 AND status IN ('starting','active','blocked') AND project_id=sqlc.arg(project_id)
ORDER BY created_at DESC,id DESC LIMIT 1;

-- name: InsertCheckNotificationWake :exec
INSERT INTO message_wake_jobs(id,message_id,recipient_agent_id,target_run_id,status,attempts,available_at,created_at,updated_at)
VALUES(sqlc.arg(id),sqlc.arg(message_id),sqlc.arg(recipient_agent_id),sqlc.arg(target_run_id),'pending',0,sqlc.arg(created_at),sqlc.arg(created_at),sqlc.arg(created_at));

-- name: AdvanceCheckNotificationThread :execrows
UPDATE message_threads SET revision=2,updated_at=sqlc.arg(updated_at),updated_by='crewfold-check-worker' WHERE id=sqlc.arg(id) AND revision=1;

-- name: ListCheckNotificationReceipts :many
SELECT id,check_result_id,freshness_revision,route_id,duty,recipient_agent_id,recipient_agent_revision,assignment_id,assignment_revision,message_id,created_at
FROM check_notification_receipts WHERE check_result_id=sqlc.arg(check_result_id) ORDER BY freshness_revision,id;

-- name: ListCheckRouteFailures :many
SELECT id,check_result_id,freshness_revision,route_id,duty,recipient_agent_id,recipient_agent_revision,assignment_id,assignment_revision,code,diagnostic,created_at
FROM check_route_failures WHERE check_result_id=sqlc.arg(check_result_id) ORDER BY freshness_revision,id;

-- name: GetCheckResultRunID :one
SELECT check_run_id FROM check_results WHERE id=sqlc.arg(check_result_id);

-- name: CheckResultIsLatestForRequirement :one
SELECT CAST(NOT EXISTS(
  SELECT 1 FROM check_results newer
  WHERE newer.requirement_id=result.requirement_id
    AND newer.requirement_revision=result.requirement_revision
    AND (newer.created_at>result.created_at OR (newer.created_at=result.created_at AND newer.id>result.id))
) AS INTEGER) AS is_latest
FROM check_results result WHERE result.id=sqlc.arg(check_result_id);

-- name: CountOpenCheckRepairProposals :one
SELECT CAST(COUNT(*) AS INTEGER) FROM check_repair_proposals
WHERE workspace_id=sqlc.arg(workspace_id) AND project_id=sqlc.arg(project_id) AND status='pending';

-- name: InsertCheckRepairProposal :exec
INSERT INTO check_repair_proposals(
 id,workspace_id,project_id,objective_id,objective_revision,task_id,task_revision,
 requirement_id,requirement_revision,check_result_id,freshness_revision,
 source_repository_id,source_checkout_id,source_head_commit,policy_revision,
 repair_launch_profile_id,repair_launch_profile_revision,source_run_id,source_agent_id,source_agent_revision,source_grant_id,source_grant_revision,
 rationale,repair_task_title,repair_task_description,repair_task_priority,
 repair_budget_tokens,repair_budget_cost_cents,repair_budget_time_seconds,recipe_sha256,
 status,revision,created_at,updated_at,created_by,updated_by
) VALUES(
 sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),sqlc.arg(objective_id),sqlc.arg(objective_revision),sqlc.arg(task_id),sqlc.arg(task_revision),
 sqlc.arg(requirement_id),sqlc.arg(requirement_revision),sqlc.arg(check_result_id),sqlc.arg(freshness_revision),
 sqlc.arg(source_repository_id),sqlc.arg(source_checkout_id),sqlc.arg(source_head_commit),sqlc.arg(policy_revision),
 sqlc.arg(repair_launch_profile_id),sqlc.arg(repair_launch_profile_revision),sqlc.arg(source_run_id),sqlc.arg(source_agent_id),sqlc.arg(source_agent_revision),sqlc.arg(source_grant_id),sqlc.arg(source_grant_revision),
 sqlc.arg(rationale),sqlc.arg(repair_task_title),sqlc.arg(repair_task_description),sqlc.arg(repair_task_priority),
 sqlc.arg(repair_budget_tokens),sqlc.arg(repair_budget_cost_cents),sqlc.arg(repair_budget_time_seconds),sqlc.arg(recipe_sha256),
 'pending',1,sqlc.arg(created_at),sqlc.arg(created_at),sqlc.arg(created_by),sqlc.arg(created_by)
);

-- name: GetCheckRepairProposal :one
SELECT id,workspace_id,project_id,objective_id,objective_revision,task_id,task_revision,
 requirement_id,requirement_revision,check_result_id,freshness_revision,
 source_repository_id,source_checkout_id,source_head_commit,policy_revision,
 repair_launch_profile_id,repair_launch_profile_revision,source_run_id,source_agent_id,source_agent_revision,source_grant_id,source_grant_revision,
 rationale,repair_task_title,repair_task_description,repair_task_priority,
 repair_budget_tokens,repair_budget_cost_cents,repair_budget_time_seconds,recipe_sha256,
 status,revision,created_at,updated_at,created_by,updated_by
FROM check_repair_proposals WHERE workspace_id=sqlc.arg(workspace_id) AND id=sqlc.arg(id);

-- name: GetLatestCheckRepairProposalByResult :one
SELECT id,workspace_id,project_id,objective_id,objective_revision,task_id,task_revision,
 requirement_id,requirement_revision,check_result_id,freshness_revision,
 source_repository_id,source_checkout_id,source_head_commit,policy_revision,
 repair_launch_profile_id,repair_launch_profile_revision,source_run_id,source_agent_id,source_agent_revision,source_grant_id,source_grant_revision,
 rationale,repair_task_title,repair_task_description,repair_task_priority,
 repair_budget_tokens,repair_budget_cost_cents,repair_budget_time_seconds,recipe_sha256,
 status,revision,created_at,updated_at,created_by,updated_by
FROM check_repair_proposals
WHERE workspace_id=sqlc.arg(workspace_id) AND check_result_id=sqlc.arg(check_result_id)
ORDER BY created_at DESC,id DESC LIMIT 1;

-- name: ListCheckRepairProposalIDs :many
SELECT id FROM check_repair_proposals
WHERE workspace_id=sqlc.arg(workspace_id)
 AND (CAST(sqlc.arg(project_id) AS TEXT)='' OR project_id=sqlc.arg(project_id))
 AND (CAST(sqlc.arg(task_id) AS TEXT)='' OR task_id=sqlc.arg(task_id))
 AND (CAST(sqlc.arg(status) AS TEXT)='' OR status=sqlc.arg(status))
ORDER BY created_at DESC,id DESC LIMIT sqlc.arg(result_limit);

-- name: UpdateCheckRepairProposalStatus :execrows
UPDATE check_repair_proposals
SET status=sqlc.arg(status),revision=revision+1,updated_at=sqlc.arg(updated_at),updated_by=sqlc.arg(updated_by)
WHERE id=sqlc.arg(id) AND workspace_id=sqlc.arg(workspace_id) AND status='pending' AND revision=sqlc.arg(expected_revision);

-- name: InsertCheckRepairDecision :exec
INSERT INTO check_repair_decisions(id,repair_proposal_id,decision,proposal_revision,note,created_at,created_by)
VALUES(sqlc.arg(id),sqlc.arg(repair_proposal_id),sqlc.arg(decision),sqlc.arg(proposal_revision),NULLIF(sqlc.arg(note),''),sqlc.arg(created_at),'local-owner');

-- name: GetCheckRepairDecision :one
SELECT id,repair_proposal_id,decision,proposal_revision,note,created_at,created_by
FROM check_repair_decisions WHERE repair_proposal_id=sqlc.arg(repair_proposal_id);

-- name: InsertCheckRepairTask :exec
INSERT INTO tasks(id,workspace_id,project_id,objective_id,title,description,status,blocked_reason,priority,budget_tokens,budget_cost_cents,budget_time_seconds,revision,created_at,updated_at,created_by,updated_by)
VALUES(sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),sqlc.arg(objective_id),sqlc.arg(title),NULLIF(sqlc.arg(description),''),'ready',NULL,sqlc.arg(priority),sqlc.arg(budget_tokens),sqlc.arg(budget_cost_cents),sqlc.arg(budget_time_seconds),1,sqlc.arg(created_at),sqlc.arg(created_at),'local-owner','local-owner');

-- name: ListCheckRepairObjectiveTaskBudgets :many
SELECT budget_tokens,budget_cost_cents,budget_time_seconds
FROM tasks
WHERE workspace_id=sqlc.arg(workspace_id) AND objective_id=sqlc.arg(objective_id) AND status<>'cancelled'
ORDER BY id;

-- name: InsertCheckRepairSchedulingIntent :exec
INSERT INTO scheduling_intents(id,workspace_id,project_id,objective_id,task_id,agent_id,launch_profile_id,source_proposal_id,source_action_id,source_check_repair_proposal_id,status,reason,assignment_id,run_id,supervisor_action_id,attempts,last_evaluated_event_sequence,revision,created_at,updated_at,next_attempt_at,created_by,updated_by)
VALUES(sqlc.arg(id),sqlc.arg(workspace_id),sqlc.arg(project_id),sqlc.arg(objective_id),sqlc.arg(task_id),sqlc.arg(agent_id),sqlc.arg(launch_profile_id),NULL,NULL,sqlc.arg(repair_proposal_id),'pending',NULL,NULL,NULL,NULL,0,0,1,sqlc.arg(created_at),sqlc.arg(created_at),NULL,'local-owner','local-owner');

-- name: InsertCheckRepairEffect :exec
INSERT INTO check_repair_effects(id,repair_proposal_id,repair_task_id,scheduling_intent_id,created_at)
VALUES(sqlc.arg(id),sqlc.arg(repair_proposal_id),sqlc.arg(repair_task_id),sqlc.arg(scheduling_intent_id),sqlc.arg(created_at));

-- name: GetCheckRepairEffect :one
SELECT id,repair_proposal_id,repair_task_id,scheduling_intent_id,created_at
FROM check_repair_effects WHERE repair_proposal_id=sqlc.arg(repair_proposal_id);

-- name: MarkPendingCheckRepairsStale :many
UPDATE check_repair_proposals
SET status='stale',revision=revision+1,updated_at=sqlc.arg(updated_at),updated_by='crewfold-check-worker'
WHERE check_result_id=sqlc.arg(check_result_id) AND status='pending'
RETURNING id,revision;

-- name: MarkSupersededCheckRepairsStale :many
UPDATE check_repair_proposals
SET status='stale',revision=revision+1,updated_at=sqlc.arg(updated_at),updated_by='crewfold-check-worker'
WHERE requirement_id=sqlc.arg(requirement_id) AND requirement_revision=sqlc.arg(requirement_revision)
 AND check_result_id<>sqlc.arg(current_check_result_id) AND status='pending'
RETURNING id,revision;

-- name: FinishTerminalCheckRun :exec
UPDATE check_runs
SET status='finished',revision=revision+1,finished_at=sqlc.arg(finished_at),
    updated_at=sqlc.arg(finished_at),updated_by='crewfold-check-worker'
WHERE id=sqlc.arg(check_run_id) AND status IN ('starting','running');

-- name: CompleteTerminalCheckJob :exec
UPDATE check_jobs
SET status='complete',lease_expires_at=NULL,updated_at=sqlc.arg(updated_at)
WHERE check_run_id=sqlc.arg(check_run_id) AND status='leased';

-- name: InsertTerminalCheckResult :exec
INSERT INTO check_results(
 id,check_run_id,requirement_id,requirement_revision,definition_id,definition_content_revision,
 outcome,exit_code,forced,diagnostic_code,diagnostic,observation_available,
 repository_id,object_format,checkout_id,branch,head_commit,dirty,dirty_paths_json,observed_at,
 observation_diagnostic_code,observation_diagnostic,created_at,created_by
) VALUES(
 sqlc.arg(id),sqlc.arg(check_run_id),sqlc.arg(requirement_id),sqlc.arg(requirement_revision),sqlc.arg(definition_id),sqlc.arg(definition_content_revision),
 sqlc.arg(outcome),sqlc.narg(exit_code),sqlc.arg(forced),sqlc.narg(diagnostic_code),sqlc.narg(diagnostic),sqlc.arg(observation_available),
 sqlc.arg(repository_id),sqlc.arg(object_format),sqlc.arg(checkout_id),sqlc.narg(branch),sqlc.narg(head_commit),sqlc.arg(dirty),sqlc.arg(dirty_paths_json),sqlc.arg(observed_at),
 sqlc.narg(observation_diagnostic_code),sqlc.narg(observation_diagnostic),sqlc.arg(created_at),'crewfold-check-worker'
);

-- name: InsertTerminalCheckArtifact :exec
INSERT INTO check_artifacts(id,check_result_id,kind,content_sha256,captured_bytes,omitted_bytes,truncated,created_at)
VALUES(sqlc.arg(id),sqlc.arg(check_result_id),sqlc.arg(kind),sqlc.arg(content_sha256),sqlc.arg(captured_bytes),sqlc.arg(omitted_bytes),sqlc.arg(truncated),sqlc.arg(created_at));

-- name: InsertInitialCheckResultFreshness :exec
INSERT INTO check_result_freshness(
 id,check_result_id,revision,status,reason,initially_eligible,ever_stale,
 observation_available,repository_id,object_format,checkout_id,branch,head_commit,dirty,dirty_paths_json,observed_at,
 diagnostic_code,diagnostic,created_at,created_by
) VALUES(
 sqlc.arg(id),sqlc.arg(check_result_id),1,sqlc.arg(status),sqlc.arg(reason),sqlc.arg(initially_eligible),sqlc.arg(ever_stale),
 sqlc.arg(observation_available),sqlc.arg(repository_id),sqlc.arg(object_format),sqlc.arg(checkout_id),sqlc.narg(branch),sqlc.narg(head_commit),sqlc.arg(dirty),sqlc.arg(dirty_paths_json),sqlc.arg(observed_at),
 sqlc.narg(diagnostic_code),sqlc.narg(diagnostic),sqlc.arg(created_at),'crewfold-check-worker'
);

-- name: InsertInitialCheckRequirementEvidence :exec
INSERT INTO check_requirement_evidence(
 id,requirement_id,requirement_revision,check_result_id,freshness_revision,class,effect,created_at,created_by
) VALUES(
 sqlc.arg(id),sqlc.arg(requirement_id),sqlc.arg(requirement_revision),sqlc.arg(check_result_id),1,'mechanical_check',sqlc.arg(effect),sqlc.arg(created_at),'crewfold-check-worker'
);

-- name: ListCheckArtifactLogMetadata :many
SELECT kind,content_sha256,captured_bytes,omitted_bytes,truncated
FROM check_artifacts
WHERE check_result_id=sqlc.arg(check_result_id)
ORDER BY kind;
