package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store/dbgen"
)

const (
	managerGrantCreatedEvent         = "manager.grant_created"
	managerGrantRevokedEvent         = "manager.grant_revoked"
	launchProfileCreatedEvent        = "manager.launch_profile_created"
	launchProfileRetiredEvent        = "manager.launch_profile_retired"
	managerProposalSubmittedEvent    = "manager.proposal_submitted"
	managerProposalAcceptedEvent     = "manager.proposal_accepted"
	managerProposalRejectedEvent     = "manager.proposal_rejected"
	managerProposalStaleEvent        = "manager.proposal_stale"
	schedulingIntentCreatedEvent     = "supervisor.intent_created"
	schedulingIntentSatisfiedEvent   = "supervisor.intent_satisfied"
	schedulingIntentFailedEvent      = "supervisor.intent_failed"
	schedulingIntentCancelledEvent   = "supervisor.intent_cancelled"
	taskClaimRequirementCreatedEvent = "task.claim_requirement_created"
	supervisorActionRecordedEvent    = "supervisor.action_recorded"
	supervisorActionAppliedEvent     = "supervisor.action_applied"
	supervisorScanCompletedEvent     = "supervisor.scan_completed"
	approvalRequestedEvent           = "approval.requested"
	approvalGrantedEvent             = "approval.granted"
	approvalDeniedEvent              = "approval.denied"
	approvalConsumedEvent            = "approval.consumed"
	maximumManagerProposalBytes      = 48 * 1024
	maximumManagerValidationIssues   = 64
	maximumSupervisorQueueCandidates = 100
	supervisorSchedulingRetryDelay   = 30 * time.Second
)

// An intent cannot be eligible before it exists. Only durable facts that make
// work ready move the eligibility instant forward; metadata-only task updates
// deliberately do not. The scheduler and Explain both use this key before
// task/intent IDs.
const schedulingIntentEligibilityKeySQL = `MAX(
  crewfold_timestamp_key(intent.created_at),
  COALESCE((
    SELECT MAX(crewfold_timestamp_key(event.occurred_at))
    FROM events event
    WHERE event.workspace_id=intent.workspace_id
      AND event.entity_type='task' AND event.entity_id=intent.task_id
      AND event.type IN ('task.readied','task.assignment_expired')
  ),crewfold_timestamp_key(intent.created_at)),
  COALESCE((
    SELECT MAX(crewfold_timestamp_key(event.occurred_at))
    FROM task_dependencies dependency
    JOIN events event ON event.workspace_id=intent.workspace_id
      AND event.entity_type='task' AND event.entity_id=dependency.depends_on_task_id
      AND event.type='task.completed'
    WHERE dependency.task_id=intent.task_id
  ),crewfold_timestamp_key(intent.created_at))
)`

// Deferred work bypasses its stable retry deadline only for a classified fact
// that can change this exact placement decision. The per-intent watermark is
// advanced on every evaluation, so an old fact cannot keep waking a permanently
// deferred queue head and starve later eligible work.
const schedulingIntentRelevantWakeSQL = `EXISTS (
  SELECT 1 FROM events event
  WHERE event.workspace_id=intent.workspace_id
    AND event.sequence>intent.last_evaluated_event_sequence AND event.sequence<=?
    AND (
      (event.type='supervisor.policy_configured' AND EXISTS (
        SELECT 1 FROM supervisor_actions deferred_action
        JOIN supervisor_action_receipts deferred_receipt ON deferred_receipt.action_id=deferred_action.id
        WHERE deferred_action.intent_id=intent.id AND deferred_action.status='deferred'
          AND deferred_receipt.condition_key=deferred_action.condition_key
          AND NOT EXISTS (SELECT 1 FROM supervisor_actions newer_action
            JOIN supervisor_action_receipts newer_receipt ON newer_receipt.action_id=newer_action.id
            WHERE newer_action.intent_id=intent.id AND newer_action.status='deferred'
              AND newer_receipt.condition_key=newer_action.condition_key
              AND newer_receipt.event_sequence>deferred_receipt.event_sequence)
          AND json_extract(deferred_action.constraint_snapshot_json,'$.failing_dimensions[0]')
            IN ('workspace_active_runs','workspace_starting_runs','project_active_runs',
              'provider_active_runs','agent_active_runs')
      ))
      OR (event.entity_type='run'
        AND event.type IN ('run.started','run.completion_proposed','run.completed','run.start_failed','run.failed','run.stopped','run.lost')
        AND EXISTS (
          SELECT 1 FROM supervisor_actions deferred_action
          JOIN supervisor_action_receipts deferred_receipt ON deferred_receipt.action_id=deferred_action.id
          JOIN runs changed_run ON changed_run.id=event.entity_id
          WHERE deferred_action.intent_id=intent.id AND deferred_action.status='deferred'
            AND deferred_receipt.condition_key=deferred_action.condition_key
            AND NOT EXISTS (SELECT 1 FROM supervisor_actions newer_action
              JOIN supervisor_action_receipts newer_receipt ON newer_receipt.action_id=newer_action.id
              WHERE newer_action.intent_id=intent.id AND newer_action.status='deferred'
                AND newer_receipt.condition_key=newer_action.condition_key
                AND newer_receipt.event_sequence>deferred_receipt.event_sequence)
            AND (
              (json_extract(deferred_action.constraint_snapshot_json,'$.failing_dimensions[0]')='workspace_active_runs'
                AND event.type IN ('run.completion_proposed','run.completed','run.start_failed','run.failed','run.stopped'))
              OR (json_extract(deferred_action.constraint_snapshot_json,'$.failing_dimensions[0]')='workspace_starting_runs'
                AND event.type IN ('run.started','run.start_failed','run.failed','run.stopped','run.lost'))
              OR (json_extract(deferred_action.constraint_snapshot_json,'$.failing_dimensions[0]')='project_active_runs'
                AND event.type IN ('run.completion_proposed','run.completed','run.start_failed','run.failed','run.stopped')
                AND changed_run.project_id=json_extract(deferred_action.constraint_snapshot_json,'$.project_active_runs.scope'))
              OR (json_extract(deferred_action.constraint_snapshot_json,'$.failing_dimensions[0]')='provider_active_runs'
                AND event.type IN ('run.completion_proposed','run.completed','run.start_failed','run.failed','run.stopped')
                AND changed_run.provider=json_extract(deferred_action.constraint_snapshot_json,'$.provider_active_runs.scope'))
              OR (json_extract(deferred_action.constraint_snapshot_json,'$.failing_dimensions[0]')='agent_active_runs'
                AND event.type IN ('run.completion_proposed','run.completed','run.start_failed','run.failed','run.stopped')
                AND changed_run.agent_id=json_extract(deferred_action.constraint_snapshot_json,'$.agent_active_runs.scope'))
              OR (json_extract(deferred_action.constraint_snapshot_json,'$.failing_dimensions[0]')='checkout'
                AND event.type IN ('run.completion_proposed','run.completed','run.start_failed','run.failed','run.stopped')
                AND EXISTS (
                  SELECT 1 FROM json_each(json_extract(deferred_action.constraint_snapshot_json,'$.checkout_candidates')) candidate
                  WHERE json_extract(candidate.value,'$.id')=changed_run.checkout_id
                ))
            )
        ))
      OR (event.entity_type='task' AND event.entity_id=intent.task_id
        AND event.type IN ('task.readied','task.assignment_expired'))
      OR (event.type='task.completed' AND EXISTS (
        SELECT 1 FROM task_dependencies dependency
        WHERE dependency.task_id=intent.task_id AND dependency.depends_on_task_id=event.entity_id
      ))
      OR (event.entity_type='claim' AND event.type IN ('claim.released','claim.expired') AND EXISTS (
        SELECT 1 FROM supervisor_actions deferred_action
        JOIN supervisor_action_receipts deferred_receipt ON deferred_receipt.action_id=deferred_action.id
        WHERE deferred_action.intent_id=intent.id AND deferred_action.status='deferred'
          AND deferred_receipt.condition_key=deferred_action.condition_key
          AND NOT EXISTS (SELECT 1 FROM supervisor_actions newer_action
            JOIN supervisor_action_receipts newer_receipt ON newer_receipt.action_id=newer_action.id
            WHERE newer_action.intent_id=intent.id AND newer_action.status='deferred'
              AND newer_receipt.condition_key=newer_action.condition_key
              AND newer_receipt.event_sequence>deferred_receipt.event_sequence)
          AND json_extract(deferred_action.constraint_snapshot_json,'$.failing_dimensions[0]')='claim'
          AND EXISTS (
            SELECT 1 FROM json_each(json_extract(deferred_action.constraint_snapshot_json,'$.claim_conflicts')) conflict
            WHERE json_extract(conflict.value,'$.conflicting_claim_id')=event.entity_id
          )
      ))
      OR (event.type='overlap.resolved' AND EXISTS (
        SELECT 1 FROM work_overlaps overlap
        WHERE overlap.id=event.entity_id AND intent.task_id IN (overlap.task_low_id,overlap.task_high_id)
      ) AND EXISTS (
        SELECT 1 FROM supervisor_actions deferred_action
        JOIN supervisor_action_receipts deferred_receipt ON deferred_receipt.action_id=deferred_action.id
        WHERE deferred_action.intent_id=intent.id AND deferred_action.status='deferred'
          AND deferred_receipt.condition_key=deferred_action.condition_key
          AND NOT EXISTS (SELECT 1 FROM supervisor_actions newer_action
            JOIN supervisor_action_receipts newer_receipt ON newer_receipt.action_id=newer_action.id
            WHERE newer_action.intent_id=intent.id AND newer_action.status='deferred'
              AND newer_receipt.condition_key=newer_action.condition_key
              AND newer_receipt.event_sequence>deferred_receipt.event_sequence)
          AND json_extract(deferred_action.constraint_snapshot_json,'$.failing_dimensions[0]')
            IN ('claim','coordination_hold')
      ))
      OR (event.entity_type='checkout' AND event.type IN ('checkout.registered','checkout.git_observed') AND EXISTS (
        SELECT 1 FROM checkouts checkout
        WHERE checkout.id=event.entity_id AND checkout.project_id=intent.project_id
          AND checkout.availability='available' AND checkout.write_mode<>'read_only'
          AND (checkout.write_mode='shared' OR NOT EXISTS (
            SELECT 1 FROM runs reserved
            WHERE reserved.checkout_id=checkout.id
              AND reserved.status IN ('requested','starting','active','blocked','stopping','lost')
          ))
      ) AND EXISTS (
        SELECT 1 FROM supervisor_actions deferred_action
        JOIN supervisor_action_receipts deferred_receipt ON deferred_receipt.action_id=deferred_action.id
        WHERE deferred_action.intent_id=intent.id AND deferred_action.status='deferred'
          AND deferred_receipt.condition_key=deferred_action.condition_key
          AND NOT EXISTS (SELECT 1 FROM supervisor_actions newer_action
            JOIN supervisor_action_receipts newer_receipt ON newer_receipt.action_id=newer_action.id
            WHERE newer_action.intent_id=intent.id AND newer_action.status='deferred'
              AND newer_receipt.condition_key=newer_action.condition_key
              AND newer_receipt.event_sequence>deferred_receipt.event_sequence)
          AND json_extract(deferred_action.constraint_snapshot_json,'$.failing_dimensions[0]')='checkout'
          AND (
            (event.type='checkout.registered' AND (
              json_extract(deferred_action.constraint_snapshot_json,'$.launch_profile.checkout_id')=''
              OR json_extract(deferred_action.constraint_snapshot_json,'$.launch_profile.checkout_id')=event.entity_id
            ))
            OR (event.type='checkout.git_observed' AND (
              json_extract(deferred_action.constraint_snapshot_json,'$.launch_profile.checkout_id')=event.entity_id
              OR EXISTS (
                SELECT 1 FROM json_each(json_extract(deferred_action.constraint_snapshot_json,'$.checkout_candidates')) candidate
                WHERE json_extract(candidate.value,'$.id')=event.entity_id
              )
            ))
          )
      ))
    )
)`

type managerGrantContent struct {
	WorkspaceID       string                             `json:"workspace_id"`
	ProjectID         string                             `json:"project_id"`
	ObjectiveID       string                             `json:"objective_id"`
	ObjectiveRevision int64                              `json:"objective_revision"`
	TaskID            string                             `json:"task_id"`
	TaskRevision      int64                              `json:"task_revision"`
	AgentID           string                             `json:"agent_id"`
	AgentRevision     int64                              `json:"agent_revision"`
	ProposalKinds     []string                           `json:"proposal_kinds"`
	LaunchProfiles    []domain.ManagerGrantLaunchProfile `json:"launch_profiles"`
	AllowedClaimKinds []string                           `json:"allowed_claim_kinds"`
	Limits            domain.ManagerProposalLimits       `json:"limits"`
	ExpiresAt         string                             `json:"expires_at,omitempty"`
}

type launchProfileContent struct {
	WorkspaceID            string `json:"workspace_id"`
	ProjectID              string `json:"project_id"`
	AgentID                string `json:"agent_id"`
	AgentRevision          int64  `json:"agent_revision"`
	Purpose                string `json:"purpose,omitempty"`
	Runtime                string `json:"runtime"`
	Provider               string `json:"provider"`
	CheckoutID             string `json:"checkout_id,omitempty"`
	ScenarioSHA256         string `json:"scenario_sha256"`
	AssignmentLeaseSeconds int64  `json:"assignment_lease_seconds"`
	CapabilityTTLSeconds   int64  `json:"capability_ttl_seconds"`
	ManagerGrantID         string `json:"manager_grant_id,omitempty"`
}

// managerProposalContent seals every immutable input that defines the proposal
// and its authority. Validation issues and lifecycle/decision fields are
// derived later and therefore deliberately do not participate.
type managerProposalContent struct {
	WorkspaceID       string                         `json:"workspace_id"`
	ProjectID         string                         `json:"project_id"`
	ObjectiveID       string                         `json:"objective_id"`
	ObjectiveRevision int64                          `json:"objective_revision"`
	SourceRunID       string                         `json:"source_run_id"`
	SourceAgentID     string                         `json:"source_agent_id"`
	GrantID           string                         `json:"grant_id"`
	GrantRevision     int64                          `json:"grant_revision"`
	Kind              string                         `json:"kind"`
	Summary           string                         `json:"summary"`
	AsOfEventSequence int64                          `json:"as_of_event_sequence"`
	Actions           []domain.ManagerProposalAction `json:"actions"`
}

func (s *Store) CreateLaunchProfile(ctx context.Context, command CreateLaunchProfileCommand) (MutationResult[domain.LaunchProfile], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProjectIdentifier = strings.TrimSpace(command.ProjectIdentifier)
	command.AgentIdentifier = strings.TrimSpace(command.AgentIdentifier)
	command.Purpose = strings.TrimSpace(command.Purpose)
	command.Runtime = strings.TrimSpace(command.Runtime)
	command.Provider = strings.TrimSpace(command.Provider)
	command.CheckoutIdentifier = strings.TrimSpace(command.CheckoutIdentifier)
	command.ManagerGrantID = strings.TrimSpace(command.ManagerGrantID)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.ProjectIdentifier == "" || command.AgentIdentifier == "" ||
		command.ExpectedAgentRevision < 1 || !validShortText(command.Runtime) || !validShortText(command.Provider) ||
		command.Purpose != "" && !validShortText(command.Purpose) ||
		command.AssignmentLeaseSeconds < 30 || command.AssignmentLeaseSeconds > 86400 ||
		command.CapabilityTTLSeconds < 30 || command.CapabilityTTLSeconds > 86400 {
		return MutationResult[domain.LaunchProfile]{}, &Error{Code: CodeInvalidLaunchProfile, Message: "launch profile requires exact project/agent revision, runtime/provider, bounded owner scenario, and lease/TTL from 30 to 86400 seconds"}
	}
	if err := execution.ValidateScenario(command.Scenario); err != nil {
		return MutationResult[domain.LaunchProfile]{}, &Error{Code: CodeInvalidLaunchProfile, Message: "launch profile scenario is invalid", Cause: err}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidLaunchProfile); err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	requestHash, err := hashManagementCommand("manager.launch_profile.create", command)
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, storageFailure("hash launch profile creation", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, storageFailure("begin launch profile creation", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.LaunchProfile]
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "manager.launch_profile.create", requestHash, &replay); err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, command.ProjectIdentifier)
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, command.AgentIdentifier)
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	if agent.Revision != command.ExpectedAgentRevision {
		return MutationResult[domain.LaunchProfile]{}, revisionConflict("agent", agent.ID, command.ExpectedAgentRevision, agent.Revision)
	}
	if !agent.Enabled || agent.Runtime != command.Runtime || agent.Provider != command.Provider {
		return MutationResult[domain.LaunchProfile]{}, &Error{Code: CodeInvalidLaunchProfile, Message: "launch profile runtime/provider must equal the exact enabled agent revision"}
	}
	checkoutID := ""
	if command.CheckoutIdentifier != "" {
		checkout, err := selectRunCheckout(ctx, tx, project.ID, command.CheckoutIdentifier)
		if err != nil {
			return MutationResult[domain.LaunchProfile]{}, err
		}
		checkoutID = checkout.ID
	}
	if command.ManagerGrantID != "" {
		grant, err := queryManagerGrant(ctx, tx, workspace.ID, command.ManagerGrantID)
		if err != nil {
			return MutationResult[domain.LaunchProfile]{}, err
		}
		if grant.Status != domain.ManagerGrantActive || grant.ProjectID != project.ID || grant.AgentID != agent.ID || grant.AgentRevision != agent.Revision ||
			grant.ExpiresAt != "" && !timestampAfterInstant(grant.ExpiresAt, s.clock().UTC()) {
			return MutationResult[domain.LaunchProfile]{}, &Error{Code: CodeInvalidLaunchProfile, Message: "management launch profile must bind the exact active grant manager agent"}
		}
		planningTask, err := queryTask(ctx, tx, workspace.ID, grant.TaskID)
		if err != nil {
			return MutationResult[domain.LaunchProfile]{}, err
		}
		if planningTask.ProjectID != grant.ProjectID || planningTask.ObjectiveID != grant.ObjectiveID ||
			planningTask.Revision != grant.TaskRevision || planningTask.Status != domain.TaskAssigned ||
			planningTask.AssignmentID == "" || planningTask.AssignedAgentID != grant.AgentID ||
			!timestampAfterInstant(planningTask.AssignmentLeaseExpiresAt, s.clock().UTC()) {
			return MutationResult[domain.LaunchProfile]{}, &Error{Code: CodeInvalidLaunchProfile, Message: "management launch profile requires the grant's exact live planning assignment"}
		}
	}
	scenarioJSON, err := json.Marshal(command.Scenario)
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, storageFailure("encode launch profile scenario", err)
	}
	scenarioDigest := sha256.Sum256(scenarioJSON)
	scenarioHash := hex.EncodeToString(scenarioDigest[:])
	content := launchProfileContent{
		WorkspaceID: workspace.ID, ProjectID: project.ID, AgentID: agent.ID, AgentRevision: agent.Revision,
		Purpose: command.Purpose, Runtime: command.Runtime, Provider: command.Provider, CheckoutID: checkoutID,
		ScenarioSHA256: scenarioHash, AssignmentLeaseSeconds: command.AssignmentLeaseSeconds,
		CapabilityTTLSeconds: command.CapabilityTTLSeconds, ManagerGrantID: command.ManagerGrantID,
	}
	contentJSON, contentHash, err := canonicalContent(content)
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, storageFailure("hash launch profile content", err)
	}
	id, err := randomID("lprof_")
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, storageFailure("generate launch profile id", err)
	}
	now := s.nowText()
	profile := domain.LaunchProfile{
		ID: id, WorkspaceID: workspace.ID, ProjectID: project.ID, AgentID: agent.ID, AgentRevision: agent.Revision,
		Purpose: command.Purpose, Runtime: command.Runtime, Provider: command.Provider, CheckoutID: checkoutID,
		Scenario: command.Scenario, ScenarioSHA256: scenarioHash, ContentSHA256: contentHash,
		AssignmentLeaseSeconds: command.AssignmentLeaseSeconds, CapabilityTTLSeconds: command.CapabilityTTLSeconds,
		ManagerGrantID: command.ManagerGrantID, Status: domain.LaunchProfileActive, Revision: 1,
		CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID,
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO launch_profiles(
  id,workspace_id,project_id,agent_id,agent_revision,purpose,runtime,provider,checkout_id,
  scenario_json,scenario_sha256,content_json,content_sha256,assignment_lease_seconds,capability_ttl_seconds,
  manager_grant_id,status,revision,created_at,updated_at,created_by,updated_by
) VALUES (?,?,?,?,?,NULLIF(?,''),?,?,NULLIF(?,''),?,?,?,?,?,?,NULLIF(?,''),'active',1,?,?,?,?)`,
		profile.ID, profile.WorkspaceID, profile.ProjectID, profile.AgentID, profile.AgentRevision, profile.Purpose,
		profile.Runtime, profile.Provider, profile.CheckoutID, string(scenarioJSON), profile.ScenarioSHA256, string(contentJSON),
		profile.ContentSHA256, profile.AssignmentLeaseSeconds, profile.CapabilityTTLSeconds, profile.ManagerGrantID,
		profile.CreatedAt, profile.UpdatedAt, profile.CreatedBy, profile.UpdatedBy); err != nil {
		return MutationResult[domain.LaunchProfile]{}, storageFailure("insert launch profile", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "launch_profile", profile.ID, 1, launchProfileCreatedEvent, command.CorrelationID, now, map[string]any{
		"project_id": profile.ProjectID, "agent_id": profile.AgentID, "agent_revision": profile.AgentRevision,
		"content_sha256": profile.ContentSHA256, "manager_grant_id": profile.ManagerGrantID,
	})
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	result := MutationResult[domain.LaunchProfile]{Value: profile, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "manager.launch_profile.create", requestHash, result, now); err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.LaunchProfile]{}, storageFailure("commit launch profile creation", err)
	}
	return result, nil
}

func (s *Store) RetireLaunchProfile(ctx context.Context, command RetireLaunchProfileCommand) (MutationResult[domain.LaunchProfile], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.LaunchProfileID = strings.TrimSpace(command.LaunchProfileID)
	command.Reason = strings.TrimSpace(command.Reason)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.LaunchProfileID == "" || command.ExpectedRevision < 1 || !validDecisionNote(command.Reason) {
		return MutationResult[domain.LaunchProfile]{}, &Error{Code: CodeInvalidLaunchProfile, Message: "launch profile retirement requires workspace, profile, expected revision, and a bounded reason"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidLaunchProfile); err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	requestHash, err := hashManagementCommand("manager.launch_profile.retire", command)
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, storageFailure("hash launch profile retirement", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, storageFailure("begin launch profile retirement", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.LaunchProfile]
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "manager.launch_profile.retire", requestHash, &replay); err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	profile, err := queryLaunchProfile(ctx, tx, workspace.ID, command.LaunchProfileID)
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	if profile.Revision != command.ExpectedRevision {
		return MutationResult[domain.LaunchProfile]{}, revisionConflict("launch profile", profile.ID, command.ExpectedRevision, profile.Revision)
	}
	if profile.Status != domain.LaunchProfileActive {
		return MutationResult[domain.LaunchProfile]{}, &Error{Code: CodeInvalidLaunchProfile, Message: "only an active launch profile can be retired"}
	}
	now := s.nowText()
	profile.Status, profile.Revision, profile.UpdatedAt, profile.UpdatedBy = domain.LaunchProfileRetired, profile.Revision+1, now, localOwnerActorID
	if _, err := tx.ExecContext(ctx, "UPDATE launch_profiles SET status='retired',revision=?,updated_at=?,updated_by=? WHERE id=?", profile.Revision, now, localOwnerActorID, profile.ID); err != nil {
		return MutationResult[domain.LaunchProfile]{}, storageFailure("retire launch profile", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "launch_profile", profile.ID, profile.Revision, launchProfileRetiredEvent, command.CorrelationID, now, map[string]any{"reason": command.Reason})
	if err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	result := MutationResult[domain.LaunchProfile]{Value: profile, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "manager.launch_profile.retire", requestHash, result, now); err != nil {
		return MutationResult[domain.LaunchProfile]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.LaunchProfile]{}, storageFailure("commit launch profile retirement", err)
	}
	return result, nil
}

func (s *Store) LaunchProfile(ctx context.Context, workspaceIdentifier, profileID string) (domain.LaunchProfile, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.LaunchProfile{}, err
	}
	return queryLaunchProfile(ctx, s.db, workspace.ID, strings.TrimSpace(profileID))
}

func (s *Store) LaunchProfiles(ctx context.Context, query ListLaunchProfilesQuery) ([]domain.LaunchProfile, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return nil, err
	}
	limit := boundedManagementLimit(query.Limit)
	statement := launchProfileSelect + " WHERE workspace_id=?"
	arguments := []any{workspace.ID}
	if value := strings.TrimSpace(query.ProjectIdentifier); value != "" {
		project, err := queryProject(ctx, s.db, workspace.ID, value)
		if err != nil {
			return nil, err
		}
		statement += " AND project_id=?"
		arguments = append(arguments, project.ID)
	}
	if value := strings.TrimSpace(query.AgentIdentifier); value != "" {
		agent, err := queryAgent(ctx, s.db, workspace.ID, value)
		if err != nil {
			return nil, err
		}
		statement += " AND agent_id=?"
		arguments = append(arguments, agent.ID)
	}
	if value := strings.TrimSpace(query.ManagerGrantID); value != "" {
		statement += " AND manager_grant_id=?"
		arguments = append(arguments, value)
	}
	if value := strings.TrimSpace(query.Status); value != "" {
		if value != domain.LaunchProfileActive && value != domain.LaunchProfileRetired {
			return nil, &Error{Code: CodeInvalidLaunchProfile, Message: "launch profile status must be active or retired"}
		}
		statement += " AND status=?"
		arguments = append(arguments, value)
	}
	statement += " ORDER BY created_at,id LIMIT ?"
	arguments = append(arguments, limit)
	rows, err := s.db.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, storageFailure("list launch profiles", err)
	}
	defer rows.Close()
	result := make([]domain.LaunchProfile, 0)
	for rows.Next() {
		profile, err := scanLaunchProfile(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, profile)
	}
	return result, rows.Err()
}

// InvokeManager atomically resolves an exact owner grant/planning-profile
// tuple, builds the sole bindable manager-authority packet, and enqueues its run using
// the immutable profile recipe. Empty tuple identifiers are accepted only
// when their scope has exactly one candidate.
func (s *Store) InvokeManager(ctx context.Context, command InvokeManagerCommand) (ManagerInvocationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ObjectiveID = strings.TrimSpace(command.ObjectiveID)
	command.TaskID = strings.TrimSpace(command.TaskID)
	command.ManagerGrantID = strings.TrimSpace(command.ManagerGrantID)
	command.LaunchProfileID = strings.TrimSpace(command.LaunchProfileID)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.ObjectiveID == "" || command.ExpectedTaskRevision < 0 || command.ExpectedGrantRevision < 0 || command.ExpectedProfileRevision < 0 {
		return ManagerInvocationResult{}, &Error{Code: CodeInvalidManagerGrant, Message: "manager invoke requires workspace, objective, and non-negative optional exact revisions"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagerGrant); err != nil {
		return ManagerInvocationResult{}, err
	}
	requestHash, err := hashManagementCommand("manager.invoke", command)
	if err != nil {
		return ManagerInvocationResult{}, storageFailure("hash manager invocation", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagerInvocationResult{}, storageFailure("begin manager invocation", err)
	}
	defer tx.Rollback()
	var replay ManagerInvocationResult
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "manager.invoke", requestHash, &replay); err != nil {
		return ManagerInvocationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return ManagerInvocationResult{}, err
	}
	objective, err := queryObjective(ctx, tx, workspace.ID, command.ObjectiveID)
	if err != nil {
		return ManagerInvocationResult{}, err
	}
	if objective.Status != domain.ObjectiveActive {
		return ManagerInvocationResult{}, &Error{Code: CodeManagerProposalDenied, Message: "manager invoke requires an active objective"}
	}
	now := s.nowText()
	grantID := command.ManagerGrantID
	if grantID == "" {
		grantID, err = uniqueStringInTransaction(ctx, tx, `SELECT id FROM manager_grants WHERE workspace_id=? AND objective_id=? AND status='active' AND (expires_at IS NULL OR crewfold_timestamp_key(expires_at)>crewfold_timestamp_key(?)) ORDER BY id LIMIT 2`, workspace.ID, objective.ID, now)
		if err != nil {
			return ManagerInvocationResult{}, &Error{Code: CodeManagerProposalConflict, Message: "manager grant is ambiguous; specify the exact grant", Cause: err}
		}
	}
	grant, err := queryManagerGrant(ctx, tx, workspace.ID, grantID)
	if err != nil {
		return ManagerInvocationResult{}, err
	}
	if grant.Status != domain.ManagerGrantActive || grant.ObjectiveID != objective.ID || command.ExpectedGrantRevision > 0 && grant.Revision != command.ExpectedGrantRevision {
		return ManagerInvocationResult{}, &Error{Code: CodeManagerProposalDenied, Message: "manager grant is not the exact current grant for the objective"}
	}
	if grant.ExpiresAt != "" {
		expires, parseErr := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
		if parseErr != nil || !expires.After(s.clock().UTC()) {
			return ManagerInvocationResult{}, &Error{Code: CodeManagerProposalDenied, Message: "manager grant has expired"}
		}
	}
	taskID := command.TaskID
	if taskID == "" {
		taskID = grant.TaskID
	}
	task, err := queryTask(ctx, tx, workspace.ID, taskID)
	if err != nil {
		return ManagerInvocationResult{}, err
	}
	if task.ID != grant.TaskID || task.ProjectID != grant.ProjectID || task.ObjectiveID != grant.ObjectiveID || task.Status != domain.TaskAssigned || task.AssignmentID == "" || task.AssignedAgentID != grant.AgentID || task.Revision != grant.TaskRevision || command.ExpectedTaskRevision > 0 && task.Revision != command.ExpectedTaskRevision {
		return ManagerInvocationResult{}, &Error{Code: CodeManagerProposalConflict, Message: "planning task is not the exact live assigned grant task"}
	}
	var assignmentExpiry string
	if err := tx.QueryRowContext(ctx, `SELECT lease_expires_at FROM task_assignments WHERE id=? AND task_id=? AND agent_id=? AND status='active'`, task.AssignmentID, task.ID, grant.AgentID).Scan(&assignmentExpiry); err != nil {
		return ManagerInvocationResult{}, &Error{Code: CodeManagerProposalConflict, Message: "planning assignment is not active", Cause: err}
	}
	expires, parseErr := time.Parse(time.RFC3339Nano, assignmentExpiry)
	if parseErr != nil || !expires.After(s.clock().UTC()) {
		return ManagerInvocationResult{}, &Error{Code: CodeManagerProposalConflict, Message: "planning assignment lease has expired"}
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, grant.AgentID)
	if err != nil {
		return ManagerInvocationResult{}, err
	}
	if !agent.Enabled || agent.Revision != grant.AgentRevision {
		return ManagerInvocationResult{}, &Error{Code: CodeManagerProposalDenied, Message: "manager agent differs from the exact enabled grant revision"}
	}
	profileID := command.LaunchProfileID
	if profileID == "" {
		profileID, err = uniqueStringInTransaction(ctx, tx, `SELECT id FROM launch_profiles WHERE workspace_id=? AND manager_grant_id=? AND status='active' ORDER BY id LIMIT 2`, workspace.ID, grant.ID)
		if err != nil {
			return ManagerInvocationResult{}, &Error{Code: CodeManagerProposalConflict, Message: "management launch profile is ambiguous; specify the exact profile", Cause: err}
		}
	}
	profile, err := queryLaunchProfile(ctx, tx, workspace.ID, profileID)
	if err != nil {
		return ManagerInvocationResult{}, err
	}
	if profile.Status != domain.LaunchProfileActive || profile.ManagerGrantID != grant.ID || profile.ProjectID != grant.ProjectID || profile.AgentID != agent.ID || profile.AgentRevision != agent.Revision || command.ExpectedProfileRevision > 0 && profile.Revision != command.ExpectedProfileRevision {
		return ManagerInvocationResult{}, &Error{Code: CodeManagerProposalConflict, Message: "management launch profile differs from the exact grant tuple"}
	}
	var liveRunID string
	if scanErr := tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE assignment_id=? AND status IN ('requested','starting','active','blocked','stopping','lost') ORDER BY id LIMIT 1`, task.AssignmentID).Scan(&liveRunID); scanErr == nil {
		return ManagerInvocationResult{}, &Error{Code: CodeRunConflict, Message: fmt.Sprintf("planning assignment already has live run %s", liveRunID)}
	} else if !errors.Is(scanErr, sql.ErrNoRows) {
		return ManagerInvocationResult{}, storageFailure("check existing manager run", scanErr)
	}
	var activeForAgent int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE agent_id=? AND status IN ('requested','starting','active','blocked','stopping','lost')`, agent.ID).Scan(&activeForAgent); err != nil {
		return ManagerInvocationResult{}, storageFailure("count manager agent runs", err)
	}
	if activeForAgent >= agent.MaxConcurrency {
		return ManagerInvocationResult{}, &Error{Code: CodePlacementUnavailable, Message: "manager agent has reached its exact concurrency limit"}
	}
	checkout, err := selectRunCheckout(ctx, tx, task.ProjectID, profile.CheckoutID)
	if err != nil {
		return ManagerInvocationResult{}, err
	}
	packet, _, err := s.buildManagerContextPacketInTransaction(ctx, tx, workspace.ID, task, agent, checkout, grant, profile, command.CorrelationID+"-context", now)
	if err != nil {
		return ManagerInvocationResult{}, err
	}
	detail, sequence, err := s.insertInvokedManagerRun(ctx, tx, workspace.ID, task, agent, checkout, packet, profile, command.CorrelationID, now, activeForAgent)
	if err != nil {
		return ManagerInvocationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return ManagerInvocationResult{}, err
	}
	result := ManagerInvocationResult{ManagerGrant: grant, LaunchProfile: profile, Detail: detail, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return ManagerInvocationResult{}, err
	}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "manager.invoke", requestHash, result, now); err != nil {
		return ManagerInvocationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManagerInvocationResult{}, storageFailure("commit manager invocation", err)
	}
	return result, nil
}

func uniqueStringInTransaction(ctx context.Context, tx *sql.Tx, statement string, arguments ...any) (string, error) {
	rows, err := tx.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	values := make([]string, 0, 2)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return "", err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(values) != 1 {
		return "", fmt.Errorf("expected exactly one candidate, found %d", len(values))
	}
	return values[0], nil
}

func (s *Store) insertInvokedManagerRun(ctx context.Context, tx *sql.Tx, workspaceID string, task domain.Task, agent domain.AgentDefinition, checkout domain.Checkout, packet domain.ContextPacket, profile domain.LaunchProfile, correlationID, now string, activeForAgent int) (domain.RunDetail, int64, error) {
	reasons := []string{
		fmt.Sprintf("owner grant fixes planning task %s and manager agent %s", task.ID, agent.ID),
		fmt.Sprintf("immutable launch profile %s fixes runtime/provider %s/%s", profile.ID, profile.Runtime, profile.Provider),
		fmt.Sprintf("agent concurrency %d/%d before placement", activeForAgent, agent.MaxConcurrency),
		fmt.Sprintf("manager context packet %s carries the exact grant snapshot", packet.ID),
	}
	scenarioJSON, err := json.Marshal(profile.Scenario)
	if err != nil {
		return domain.RunDetail{}, 0, storageFailure("encode manager run scenario", err)
	}
	reasonsJSON, _ := json.Marshal(reasons)
	runID, err := randomID("run_")
	if err != nil {
		return domain.RunDetail{}, 0, storageFailure("generate manager run id", err)
	}
	run := domain.Run{ID: runID, WorkspaceID: workspaceID, ProjectID: task.ProjectID, TaskID: task.ID, AssignmentID: task.AssignmentID,
		AgentID: agent.ID, CheckoutID: checkout.ID, ContextPacketID: packet.ID, Runtime: profile.Runtime, Provider: profile.Provider,
		ScenarioName: profile.Scenario.Name, Status: domain.RunRequested, Revision: 1, CreatedAt: now, UpdatedAt: now,
		CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID,
		Placement: domain.RunPlacement{TaskID: task.ID, AgentID: agent.ID, CheckoutID: checkout.ID, CheckoutPath: checkout.Path, WriteMode: checkout.WriteMode, Runtime: profile.Runtime, Provider: profile.Provider, Reasons: reasons}}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id,workspace_id,project_id,task_id,agent_id,checkout_id,runtime,provider,scenario_name,scenario_json,placement_reasons_json,status,step_cursor,revision,created_at,updated_at,created_by,updated_by,assignment_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,'requested',0,1,?,?,?,?,?)`,
		run.ID, run.WorkspaceID, run.ProjectID, run.TaskID, run.AgentID, run.CheckoutID, run.Runtime, run.Provider, run.ScenarioName, string(scenarioJSON), string(reasonsJSON), now, now, run.CreatedBy, run.UpdatedBy, run.AssignmentID); err != nil {
		return domain.RunDetail{}, 0, storageFailure("insert manager run", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_context_bindings(run_id,context_packet_id,bound_at) VALUES (?,?,?)`, run.ID, packet.ID, now); err != nil {
		return domain.RunDetail{}, 0, storageFailure("bind manager context", err)
	}
	if err := dbgen.New(tx).InsertRunContextDeltaState(ctx, dbgen.InsertRunContextDeltaStateParams{RunID: run.ID, ContextPacketID: packet.ID, ScanEventSequence: packet.AsOfEventSequence, CreatedAt: now, UpdatedAt: now}); err != nil {
		return domain.RunDetail{}, 0, storageFailure("initialize manager context delta", err)
	}
	expiry := s.clock().UTC().Add(time.Duration(profile.CapabilityTTLSeconds) * time.Second).Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_capabilities(run_id,expires_at,created_at) VALUES (?,?,?)`, run.ID, expiry, now); err != nil {
		return domain.RunDetail{}, 0, storageFailure("create manager run capability", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_jobs(run_id,status,available_at,attempts,created_at,updated_at,origin) VALUES (?,'pending',?,0,?,?,'owner')`, run.ID, now, now, now); err != nil {
		return domain.RunDetail{}, 0, storageFailure("enqueue manager run", err)
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runRequestedEvent, "manager run accepted for asynchronous execution", nil, now); err != nil {
		return domain.RunDetail{}, 0, err
	}
	sequence, err := appendEvent(ctx, tx, workspaceID, "run", run.ID, 1, runRequestedEvent, correlationID, now, map[string]any{"placement": run.Placement, "scenario": run.ScenarioName, "launch_profile_id": profile.ID})
	if err != nil {
		return domain.RunDetail{}, 0, err
	}
	detail, err := runDetailInTransaction(ctx, tx, run)
	return detail, sequence, err
}

func (s *Store) CreateManagerGrant(ctx context.Context, command CreateManagerGrantCommand) (MutationResult[domain.ManagerGrant], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProjectIdentifier = strings.TrimSpace(command.ProjectIdentifier)
	command.ObjectiveID = strings.TrimSpace(command.ObjectiveID)
	command.TaskID = strings.TrimSpace(command.TaskID)
	command.AgentIdentifier = strings.TrimSpace(command.AgentIdentifier)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	command.ExpiresAt = strings.TrimSpace(command.ExpiresAt)
	if command.WorkspaceIdentifier == "" || command.ProjectIdentifier == "" || command.ObjectiveID == "" || command.TaskID == "" ||
		command.AgentIdentifier == "" || command.ExpectedTaskRevision < 1 || command.ExpectedAgentRevision < 1 ||
		!validManagerProposalLimits(command.Limits) {
		return MutationResult[domain.ManagerGrant]{}, &Error{Code: CodeInvalidManagerGrant, Message: "manager grant requires exact workspace/project/objective/task/agent revisions and positive bounded proposal limits"}
	}
	if command.ExpiresAt != "" && !canonicalTimestamp(command.ExpiresAt) {
		return MutationResult[domain.ManagerGrant]{}, &Error{Code: CodeInvalidManagerGrant, Message: "manager grant expiry must be a canonical UTC RFC3339 timestamp"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagerGrant); err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	kinds, err := canonicalProposalKinds(command.ProposalKinds)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	claimKinds, err := canonicalClaimKinds(command.AllowedClaimKinds)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	profileIDs, err := canonicalIDs(command.LaunchProfileIDs, 1, 32, "launch profiles", CodeInvalidManagerGrant)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	command.ProposalKinds, command.AllowedClaimKinds, command.LaunchProfileIDs = kinds, claimKinds, profileIDs
	requestHash, err := hashManagementCommand("manager.grant.create", command)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, storageFailure("hash manager grant creation", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, storageFailure("begin manager grant creation", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.ManagerGrant]
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "manager.grant.create", requestHash, &replay); err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, command.ProjectIdentifier)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	objective, err := queryObjective(ctx, tx, workspace.ID, command.ObjectiveID)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	task, err := queryTask(ctx, tx, workspace.ID, command.TaskID)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	agent, err := queryAgent(ctx, tx, workspace.ID, command.AgentIdentifier)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	if project.ID != objective.ProjectID || task.ProjectID != project.ID || task.ObjectiveID != objective.ID || objective.Status != domain.ObjectiveActive ||
		task.Revision != command.ExpectedTaskRevision || task.AssignmentID == "" || task.AssignedAgentID != agent.ID ||
		!timestampAfterInstant(task.AssignmentLeaseExpiresAt, s.clock().UTC()) || agent.Revision != command.ExpectedAgentRevision || !agent.Enabled {
		return MutationResult[domain.ManagerGrant]{}, &Error{Code: CodeInvalidManagerGrant, Message: "manager grant scope, revisions, or active task assignment are not exact"}
	}
	if command.ExpiresAt != "" {
		expires, _ := time.Parse(time.RFC3339Nano, command.ExpiresAt)
		if !expires.After(s.clock().UTC()) {
			return MutationResult[domain.ManagerGrant]{}, &Error{Code: CodeInvalidManagerGrant, Message: "manager grant expiry must be in the future"}
		}
	}
	profiles := make([]domain.ManagerGrantLaunchProfile, 0, len(profileIDs))
	for _, profileID := range profileIDs {
		profile, err := queryLaunchProfile(ctx, tx, workspace.ID, profileID)
		if err != nil {
			return MutationResult[domain.ManagerGrant]{}, err
		}
		targetAgent, err := queryAgent(ctx, tx, workspace.ID, profile.AgentID)
		if err != nil {
			return MutationResult[domain.ManagerGrant]{}, err
		}
		if profile.ProjectID != project.ID || profile.Status != domain.LaunchProfileActive || profile.ManagerGrantID != "" ||
			!targetAgent.Enabled || targetAgent.Revision != profile.AgentRevision {
			return MutationResult[domain.ManagerGrant]{}, &Error{Code: CodeInvalidManagerGrant, Message: "target launch profiles must be active project profiles without management authority"}
		}
		profiles = append(profiles, domain.ManagerGrantLaunchProfile{LaunchProfileID: profile.ID, Revision: profile.Revision, AgentID: profile.AgentID, AgentRevision: profile.AgentRevision})
	}
	id, err := randomID("mgrgrant_")
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, storageFailure("generate manager grant id", err)
	}
	content := managerGrantContent{
		WorkspaceID: workspace.ID, ProjectID: project.ID, ObjectiveID: objective.ID, ObjectiveRevision: objective.Revision, TaskID: task.ID, TaskRevision: task.Revision,
		AgentID: agent.ID, AgentRevision: agent.Revision, ProposalKinds: kinds, LaunchProfiles: profiles,
		AllowedClaimKinds: claimKinds, Limits: command.Limits, ExpiresAt: command.ExpiresAt,
	}
	contentJSON, contentHash, err := canonicalContent(content)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, storageFailure("hash manager grant content", err)
	}
	proposalKindsJSON, _ := json.Marshal(kinds)
	profilesJSON, _ := json.Marshal(profiles)
	claimKindsJSON, _ := json.Marshal(claimKinds)
	for ordinal, kind := range kinds {
		if _, err := tx.ExecContext(ctx, "INSERT INTO manager_grant_proposal_kinds(grant_id,ordinal,kind) VALUES (?,?,?)", id, ordinal, kind); err != nil {
			return MutationResult[domain.ManagerGrant]{}, storageFailure("insert manager proposal kind", err)
		}
	}
	for ordinal, profile := range profiles {
		if _, err := tx.ExecContext(ctx, `INSERT INTO manager_grant_launch_profiles(grant_id,ordinal,launch_profile_id,launch_profile_revision,agent_id,agent_revision) VALUES (?,?,?,?,?,?)`, id, ordinal, profile.LaunchProfileID, profile.Revision, profile.AgentID, profile.AgentRevision); err != nil {
			return MutationResult[domain.ManagerGrant]{}, storageFailure("insert manager grant launch profile", err)
		}
	}
	for ordinal, kind := range claimKinds {
		if _, err := tx.ExecContext(ctx, "INSERT INTO manager_grant_claim_kinds(grant_id,ordinal,kind) VALUES (?,?,?)", id, ordinal, kind); err != nil {
			return MutationResult[domain.ManagerGrant]{}, storageFailure("insert manager claim kind", err)
		}
	}
	now := s.nowText()
	grant := domain.ManagerGrant{
		ID: id, WorkspaceID: workspace.ID, ProjectID: project.ID, ObjectiveID: objective.ID, ObjectiveRevision: objective.Revision, TaskID: task.ID,
		TaskRevision: task.Revision, AgentID: agent.ID, AgentRevision: agent.Revision, ProposalKinds: kinds,
		LaunchProfiles: profiles, AllowedClaimKinds: claimKinds, Limits: command.Limits, ContentSHA256: contentHash,
		ExpiresAt: command.ExpiresAt, Status: domain.ManagerGrantActive, Revision: 1,
		CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID,
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO manager_grants(
 id,workspace_id,project_id,objective_id,objective_revision,task_id,task_revision,agent_id,agent_revision,
 proposal_kinds_json,launch_profiles_json,allowed_claim_kinds_json,max_open_proposals,max_actions,max_tasks,
 max_dependencies,max_claim_requirements,budget_tokens,budget_cost_cents,budget_time_seconds,content_json,content_sha256,
 expires_at,status,revision,created_at,updated_at,created_by,updated_by
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULLIF(?,''),'active',1,?,?,?,?)`,
		grant.ID, grant.WorkspaceID, grant.ProjectID, grant.ObjectiveID, grant.ObjectiveRevision, grant.TaskID, grant.TaskRevision, grant.AgentID, grant.AgentRevision,
		string(proposalKindsJSON), string(profilesJSON), string(claimKindsJSON), grant.Limits.MaxOpenProposals, grant.Limits.MaxActions,
		grant.Limits.MaxTasks, grant.Limits.MaxDependencies, grant.Limits.MaxClaimRequirements, grant.Limits.Budget.TokenLimit,
		grant.Limits.Budget.CostCents, grant.Limits.Budget.TimeSeconds, string(contentJSON), grant.ContentSHA256, grant.ExpiresAt,
		grant.CreatedAt, grant.UpdatedAt, grant.CreatedBy, grant.UpdatedBy); err != nil {
		return MutationResult[domain.ManagerGrant]{}, storageFailure("insert manager grant", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "manager_grant", grant.ID, 1, managerGrantCreatedEvent, command.CorrelationID, now, map[string]any{
		"project_id": grant.ProjectID, "objective_id": grant.ObjectiveID, "task_id": grant.TaskID, "agent_id": grant.AgentID,
		"content_sha256": grant.ContentSHA256,
	})
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	result := MutationResult[domain.ManagerGrant]{Value: grant, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "manager.grant.create", requestHash, result, now); err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagerGrant]{}, storageFailure("commit manager grant creation", err)
	}
	return result, nil
}

func (s *Store) RevokeManagerGrant(ctx context.Context, command RevokeManagerGrantCommand) (MutationResult[domain.ManagerGrant], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ManagerGrantID = strings.TrimSpace(command.ManagerGrantID)
	command.Reason = strings.TrimSpace(command.Reason)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.ManagerGrantID == "" || command.ExpectedRevision < 1 || !validDecisionNote(command.Reason) {
		return MutationResult[domain.ManagerGrant]{}, &Error{Code: CodeInvalidManagerGrant, Message: "grant revocation requires workspace, grant, expected revision, and a bounded reason"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagerGrant); err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	requestHash, err := hashManagementCommand("manager.grant.revoke", command)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, storageFailure("hash manager grant revocation", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, storageFailure("begin manager grant revocation", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.ManagerGrant]
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "manager.grant.revoke", requestHash, &replay); err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	grant, err := queryManagerGrant(ctx, tx, workspace.ID, command.ManagerGrantID)
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	if grant.Revision != command.ExpectedRevision {
		return MutationResult[domain.ManagerGrant]{}, revisionConflict("manager grant", grant.ID, command.ExpectedRevision, grant.Revision)
	}
	if grant.Status != domain.ManagerGrantActive {
		return MutationResult[domain.ManagerGrant]{}, &Error{Code: CodeManagerGrantDenied, Message: "only an active manager grant can be revoked"}
	}
	now := s.nowText()
	grant.Status, grant.Revision, grant.UpdatedAt, grant.UpdatedBy = domain.ManagerGrantRevoked, grant.Revision+1, now, localOwnerActorID
	if _, err := tx.ExecContext(ctx, "UPDATE manager_grants SET status='revoked',revision=?,updated_at=?,updated_by=? WHERE id=?", grant.Revision, now, localOwnerActorID, grant.ID); err != nil {
		return MutationResult[domain.ManagerGrant]{}, storageFailure("revoke manager grant", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "manager_grant", grant.ID, grant.Revision, managerGrantRevokedEvent, command.CorrelationID, now, map[string]any{"reason": command.Reason})
	if err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	result := MutationResult[domain.ManagerGrant]{Value: grant, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "manager.grant.revoke", requestHash, result, now); err != nil {
		return MutationResult[domain.ManagerGrant]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.ManagerGrant]{}, storageFailure("commit manager grant revocation", err)
	}
	return result, nil
}

func (s *Store) ManagerGrant(ctx context.Context, workspaceIdentifier, grantID string) (domain.ManagerGrant, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.ManagerGrant{}, err
	}
	return queryManagerGrant(ctx, s.db, workspace.ID, strings.TrimSpace(grantID))
}

func (s *Store) ManagerGrants(ctx context.Context, query ListManagerGrantsQuery) ([]domain.ManagerGrant, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return nil, err
	}
	statement := managerGrantSelect + " WHERE workspace_id=?"
	arguments := []any{workspace.ID}
	filters := []struct{ clause, value string }{
		{"project_id=?", strings.TrimSpace(query.ProjectIdentifier)}, {"objective_id=?", strings.TrimSpace(query.ObjectiveID)},
		{"task_id=?", strings.TrimSpace(query.TaskID)}, {"agent_id=?", strings.TrimSpace(query.AgentIdentifier)}, {"status=?", strings.TrimSpace(query.Status)},
	}
	for _, filter := range filters {
		if filter.value != "" {
			statement += " AND " + filter.clause
			arguments = append(arguments, filter.value)
		}
	}
	statement += " ORDER BY created_at,id LIMIT ?"
	arguments = append(arguments, boundedManagementLimit(query.Limit))
	rows, err := s.db.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, storageFailure("list manager grants", err)
	}
	defer rows.Close()
	result := make([]domain.ManagerGrant, 0)
	for rows.Next() {
		grant, err := scanManagerGrant(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, grant)
	}
	return result, rows.Err()
}

func (s *Store) SubmitManagerProposal(ctx context.Context, command SubmitManagerProposalCommand) (ManagerProposalMutationResult, error) {
	command.RunID = strings.TrimSpace(command.RunID)
	command.ManagerGrantID = strings.TrimSpace(command.ManagerGrantID)
	command.Kind = strings.TrimSpace(command.Kind)
	command.Summary = strings.TrimSpace(command.Summary)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.RunID == "" || command.ManagerGrantID == "" || command.ExpectedGrantRevision < 1 || !validManagerProposalKind(command.Kind) ||
		!validManagerText(command.Summary, 1024) || command.AsOfEventSequence < 0 || len(command.Actions) < 1 || len(command.Actions) > 32 {
		return ManagerProposalMutationResult{}, &Error{Code: CodeInvalidManagerProposal, Message: "manager proposal requires authenticated run/grant, kind, summary, cursor, and 1 to 32 actions"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagerProposal); err != nil {
		return ManagerProposalMutationResult{}, err
	}
	for index := range command.Actions {
		if command.Actions[index].ID != "" || command.Actions[index].Ordinal != 0 {
			return ManagerProposalMutationResult{}, &Error{Code: CodeInvalidManagerProposal, Message: "proposal action IDs and ordinals are assigned by Crewfold"}
		}
	}
	requestHash, err := hashManagementCommand("manager.proposal.submit", command)
	if err != nil {
		return ManagerProposalMutationResult{}, storageFailure("hash manager proposal submission", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagerProposalMutationResult{}, storageFailure("begin manager proposal submission", err)
	}
	defer tx.Rollback()
	var replay ManagerProposalMutationResult
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "manager.proposal.submit", requestHash, &replay); err != nil {
		return ManagerProposalMutationResult{}, err
	} else if found {
		return replay, nil
	}
	for index := range command.Actions {
		id, err := randomID("mpact_")
		if err != nil {
			return ManagerProposalMutationResult{}, storageFailure("generate proposal action id", err)
		}
		command.Actions[index].ID, command.Actions[index].Ordinal = id, index
	}
	run, err := queryRun(ctx, tx, "", command.RunID)
	if err != nil {
		return ManagerProposalMutationResult{}, err
	}
	grant, err := queryManagerGrant(ctx, tx, run.WorkspaceID, command.ManagerGrantID)
	if err != nil {
		return ManagerProposalMutationResult{}, err
	}
	now := s.nowText()
	if err := validateProposalAuthority(ctx, tx, run, grant, command, now); err != nil {
		return ManagerProposalMutationResult{}, err
	}
	actionsJSON, err := json.Marshal(command.Actions)
	if err != nil || len(actionsJSON) > maximumManagerProposalBytes {
		return ManagerProposalMutationResult{}, &Error{Code: CodeInvalidManagerProposal, Message: "proposal actions exceed the 48 KiB encoded bound"}
	}
	issues, err := s.validateManagerProposalInTransaction(ctx, tx, grant, command.Kind, command.Actions)
	if err != nil {
		return ManagerProposalMutationResult{}, err
	}
	status := domain.ManagerProposalPending
	if proposalHasErrors(issues) {
		status = domain.ManagerProposalInvalid
	}
	issuesJSON, _ := json.Marshal(issues)
	proposalID, err := randomID("mprop_")
	if err != nil {
		return ManagerProposalMutationResult{}, storageFailure("generate manager proposal id", err)
	}
	actorID := "agent:" + run.AgentID
	contentJSON, contentHash, err := canonicalContent(managerProposalContent{
		WorkspaceID: run.WorkspaceID, ProjectID: grant.ProjectID, ObjectiveID: grant.ObjectiveID, ObjectiveRevision: grant.ObjectiveRevision,
		SourceRunID: run.ID, SourceAgentID: run.AgentID, GrantID: grant.ID, GrantRevision: grant.Revision,
		Kind: command.Kind, Summary: command.Summary, AsOfEventSequence: command.AsOfEventSequence, Actions: command.Actions,
	})
	if err != nil {
		return ManagerProposalMutationResult{}, storageFailure("hash manager proposal content", err)
	}
	if len(contentJSON) > maximumManagerProposalBytes {
		return ManagerProposalMutationResult{}, &Error{Code: CodeInvalidManagerProposal, Message: "canonical proposal exceeds the 48 KiB encoded bound"}
	}
	proposal := domain.ManagerProposal{
		ID: proposalID, WorkspaceID: run.WorkspaceID, ProjectID: grant.ProjectID, ObjectiveID: grant.ObjectiveID, ObjectiveRevision: grant.ObjectiveRevision,
		SourceRunID: run.ID, SourceAgentID: run.AgentID, GrantID: grant.ID, GrantRevision: grant.Revision,
		Kind: command.Kind, Summary: command.Summary, Status: status, AsOfEventSequence: command.AsOfEventSequence,
		Actions: command.Actions, ValidationIssues: issues, ContentSHA256: contentHash, Revision: 1,
		CreatedAt: now, UpdatedAt: now, CreatedBy: actorID, UpdatedBy: actorID,
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO manager_proposals(
 id,workspace_id,project_id,objective_id,objective_revision,source_run_id,source_agent_id,grant_id,grant_revision,
 kind,summary,status,as_of_event_sequence,actions_json,validation_issues_json,content_json,content_sha256,revision,
 created_at,updated_at,created_by,updated_by
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,?)`,
		proposal.ID, proposal.WorkspaceID, proposal.ProjectID, proposal.ObjectiveID, proposal.ObjectiveRevision, proposal.SourceRunID, proposal.SourceAgentID,
		proposal.GrantID, proposal.GrantRevision, proposal.Kind, proposal.Summary, proposal.Status, proposal.AsOfEventSequence,
		string(actionsJSON), string(issuesJSON), string(contentJSON), proposal.ContentSHA256, proposal.CreatedAt, proposal.UpdatedAt, proposal.CreatedBy, proposal.UpdatedBy); err != nil {
		return ManagerProposalMutationResult{}, storageFailure("insert manager proposal", err)
	}
	for _, action := range proposal.Actions {
		payload, err := proposalActionPayload(action)
		if err != nil {
			return ManagerProposalMutationResult{}, err
		}
		payloadJSON, _ := json.Marshal(payload)
		payloadDigest := sha256.Sum256(payloadJSON)
		if _, err := tx.ExecContext(ctx, `INSERT INTO manager_proposal_actions(id,proposal_id,ordinal,type,payload_json,content_sha256,created_at,created_by) VALUES (?,?,?,?,?,?,?,?)`,
			action.ID, proposal.ID, action.Ordinal, action.Type, string(payloadJSON), hex.EncodeToString(payloadDigest[:]), now, actorID); err != nil {
			return ManagerProposalMutationResult{}, storageFailure("insert manager proposal action", err)
		}
	}
	if err := s.runMutationHook(MutationAfterProposalActions); err != nil {
		return ManagerProposalMutationResult{}, err
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return ManagerProposalMutationResult{}, err
	}
	sequence, err := appendEventForActor(ctx, tx, run.WorkspaceID, "manager_proposal", proposal.ID, 1, managerProposalSubmittedEvent, command.CorrelationID, now, actorID, "agent_run", map[string]any{
		"run_id": run.ID, "grant_id": grant.ID, "kind": proposal.Kind, "status": proposal.Status,
		"content_sha256": proposal.ContentSHA256, "action_count": len(proposal.Actions),
	})
	if err != nil {
		return ManagerProposalMutationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO manager_proposal_submissions(proposal_id,action_count,content_sha256,event_sequence,submitted_at,submitted_by) VALUES (?,?,?,?,?,?)`, proposal.ID, len(proposal.Actions), proposal.ContentSHA256, sequence, now, actorID); err != nil {
		return ManagerProposalMutationResult{}, storageFailure("seal manager proposal submission", err)
	}
	if err := s.runMutationHook(MutationAfterProposalSubmission); err != nil {
		return ManagerProposalMutationResult{}, err
	}
	result := ManagerProposalMutationResult{Proposal: proposal, Effects: []domain.ManagerProposalEffect{}, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return ManagerProposalMutationResult{}, err
	}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "manager.proposal.submit", requestHash, result, now); err != nil {
		return ManagerProposalMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManagerProposalMutationResult{}, storageFailure("commit manager proposal submission", err)
	}
	return result, nil
}

func (s *Store) ManagerProposal(ctx context.Context, workspaceIdentifier, proposalID string) (domain.ManagerProposal, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.ManagerProposal{}, err
	}
	return queryManagerProposal(ctx, s.db, workspace.ID, strings.TrimSpace(proposalID))
}

func (s *Store) AcceptManagerProposal(ctx context.Context, command AcceptManagerProposalCommand) (ManagerProposalMutationResult, error) {
	return s.decideManagerProposal(ctx, command, true)
}

func (s *Store) RejectManagerProposal(ctx context.Context, command RejectManagerProposalCommand) (ManagerProposalMutationResult, error) {
	return s.decideManagerProposal(ctx, command, false)
}

func (s *Store) decideManagerProposal(ctx context.Context, command AcceptManagerProposalCommand, accept bool) (ManagerProposalMutationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ManagerProposalID = strings.TrimSpace(command.ManagerProposalID)
	command.DecisionNote = strings.TrimSpace(command.DecisionNote)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.ManagerProposalID == "" || command.ExpectedRevision < 1 || !validDecisionNote(command.DecisionNote) {
		return ManagerProposalMutationResult{}, &Error{Code: CodeInvalidManagerProposal, Message: "proposal decision requires workspace, proposal, expected revision, and a bounded decision note"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidManagerProposal); err != nil {
		return ManagerProposalMutationResult{}, err
	}
	operation := "manager.proposal.reject"
	if accept {
		operation = "manager.proposal.accept"
	}
	requestHash, err := hashManagementCommand(operation, command)
	if err != nil {
		return ManagerProposalMutationResult{}, storageFailure("hash manager proposal decision", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagerProposalMutationResult{}, storageFailure("begin manager proposal decision", err)
	}
	defer tx.Rollback()
	var replay ManagerProposalMutationResult
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, operation, requestHash, &replay); err != nil {
		return ManagerProposalMutationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return ManagerProposalMutationResult{}, err
	}
	proposal, err := queryManagerProposal(ctx, tx, workspace.ID, command.ManagerProposalID)
	if err != nil {
		return ManagerProposalMutationResult{}, err
	}
	if proposal.Revision != command.ExpectedRevision {
		return ManagerProposalMutationResult{}, revisionConflict("manager proposal", proposal.ID, command.ExpectedRevision, proposal.Revision)
	}
	if proposal.Status != domain.ManagerProposalPending && (accept || proposal.Status != domain.ManagerProposalInvalid) {
		return ManagerProposalMutationResult{}, &Error{Code: CodeManagerProposalConflict, Message: fmt.Sprintf("only a pending proposal can be accepted; pending or invalid proposals can be rejected; %s is %s", proposal.ID, proposal.Status)}
	}
	now := s.nowText()
	effects := make([]domain.ManagerProposalEffect, 0)
	status, eventType := domain.ManagerProposalRejected, managerProposalRejectedEvent
	if accept {
		grant, err := queryManagerGrant(ctx, tx, workspace.ID, proposal.GrantID)
		if err != nil {
			return ManagerProposalMutationResult{}, err
		}
		issues, validationErr := s.validateManagerProposalInTransaction(ctx, tx, grant, proposal.Kind, proposal.Actions)
		grantCurrent := grant.Status == domain.ManagerGrantActive && grant.Revision == proposal.GrantRevision &&
			grant.ObjectiveRevision == proposal.ObjectiveRevision
		objective, objectiveErr := queryObjective(ctx, tx, workspace.ID, proposal.ObjectiveID)
		grantCurrent = grantCurrent && objectiveErr == nil && objective.ProjectID == proposal.ProjectID &&
			objective.Status == domain.ObjectiveActive && objective.Revision == proposal.ObjectiveRevision
		sourceAgent, sourceAgentErr := queryAgent(ctx, tx, workspace.ID, proposal.SourceAgentID)
		grantCurrent = grantCurrent && sourceAgentErr == nil && sourceAgent.Enabled &&
			sourceAgent.Revision == grant.AgentRevision && sourceAgent.ID == grant.AgentID
		var sourcePacketGrantID string
		var sourcePacketGrantRevision, sourcePacketObjectiveRevision int64
		packetErr := tx.QueryRowContext(ctx, `SELECT json_extract(packet.packet_json,'$.management_grant.grant_id'),
json_extract(packet.packet_json,'$.management_grant.grant_revision'),json_extract(packet.packet_json,'$.management_grant.objective_revision')
FROM run_context_bindings binding JOIN context_packets packet ON packet.id=binding.context_packet_id
WHERE binding.run_id=? AND json_extract(packet.packet_json,'$.schema')=?`, proposal.SourceRunID, domain.ContextPacketSchema).
			Scan(&sourcePacketGrantID, &sourcePacketGrantRevision, &sourcePacketObjectiveRevision)
		grantCurrent = grantCurrent && packetErr == nil && sourcePacketGrantID == proposal.GrantID &&
			sourcePacketGrantRevision == proposal.GrantRevision && sourcePacketObjectiveRevision == proposal.ObjectiveRevision
		if grant.ExpiresAt != "" {
			expires, expiryErr := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
			grantCurrent = grantCurrent && expiryErr == nil && expires.After(s.clock().UTC())
		}
		if validationErr != nil {
			return ManagerProposalMutationResult{}, validationErr
		}
		if !grantCurrent || proposalHasErrors(issues) {
			status, eventType = domain.ManagerProposalStale, managerProposalStaleEvent
			if !grantCurrent {
				issues = append(issues, domain.ProposalValidationIssue{
					Code: "grant_stale", Path: "grant", Message: "manager grant is revoked, expired, or no longer at the accepted revision", Severity: domain.ProposalIssueError,
				})
			}
			proposal.ValidationIssues = issues
		} else {
			status, eventType = domain.ManagerProposalAccepted, managerProposalAcceptedEvent
		}
	}
	proposal.Status, proposal.DecisionNote, proposal.Revision = status, command.DecisionNote, proposal.Revision+1
	proposal.UpdatedAt, proposal.DecidedAt, proposal.UpdatedBy, proposal.DecidedBy = now, now, localOwnerActorID, localOwnerActorID
	issuesJSON, err := json.Marshal(proposal.ValidationIssues)
	if err != nil {
		return ManagerProposalMutationResult{}, storageFailure("encode proposal decision issues", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE manager_proposals SET status=?,validation_issues_json=?,decision_note=?,revision=?,updated_at=?,decided_at=?,updated_by=?,decided_by=? WHERE id=?`,
		proposal.Status, string(issuesJSON), proposal.DecisionNote, proposal.Revision, now, now, localOwnerActorID, localOwnerActorID, proposal.ID); err != nil {
		return ManagerProposalMutationResult{}, storageFailure("decide manager proposal", err)
	}
	if proposal.Status == domain.ManagerProposalAccepted {
		effects, err = s.applyManagerProposalInTransaction(ctx, tx, proposal, command.CorrelationID, now)
		if err != nil {
			return ManagerProposalMutationResult{}, err
		}
		if err := s.runMutationHook(MutationAfterProposalEffects); err != nil {
			return ManagerProposalMutationResult{}, err
		}
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return ManagerProposalMutationResult{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "manager_proposal", proposal.ID, proposal.Revision, eventType, command.CorrelationID, now, map[string]any{
		"status": proposal.Status, "decision_note": proposal.DecisionNote, "effect_count": len(effects),
	})
	if err != nil {
		return ManagerProposalMutationResult{}, err
	}
	effectsJSON, err := canonicalProposalEffectsJSON(effects)
	if err != nil {
		return ManagerProposalMutationResult{}, storageFailure("encode manager proposal effects receipt", err)
	}
	effectsDigest := sha256.Sum256(effectsJSON)
	if _, err := tx.ExecContext(ctx, `INSERT INTO manager_proposal_decisions(proposal_id,status,proposal_revision,effect_count,effects_sha256,event_sequence,decided_at,decided_by) VALUES (?,?,?,?,?,?,?,?)`,
		proposal.ID, proposal.Status, proposal.Revision, len(effects), hex.EncodeToString(effectsDigest[:]), sequence, now, localOwnerActorID); err != nil {
		return ManagerProposalMutationResult{}, storageFailure("seal manager proposal decision", err)
	}
	if err := s.runMutationHook(MutationAfterProposalDecision); err != nil {
		return ManagerProposalMutationResult{}, err
	}
	result := ManagerProposalMutationResult{Proposal: proposal, Effects: effects, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return ManagerProposalMutationResult{}, err
	}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, operation, requestHash, result, now); err != nil {
		return ManagerProposalMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManagerProposalMutationResult{}, storageFailure("commit manager proposal decision", err)
	}
	return result, nil
}

func (s *Store) applyManagerProposalInTransaction(ctx context.Context, tx *sql.Tx, proposal domain.ManagerProposal, correlationID, now string) ([]domain.ManagerProposalEffect, error) {
	createdTasks := make(map[string]domain.Task)
	effects := make([]domain.ManagerProposalEffect, 0, len(proposal.Actions)*2)
	addEffect := func(actionID, effectType, entityType, entityID string, eventSequence int64) error {
		id, err := randomID("mpeff_")
		if err != nil {
			return storageFailure("generate manager proposal effect id", err)
		}
		effect := domain.ManagerProposalEffect{ID: id, WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID, ObjectiveID: proposal.ObjectiveID,
			ProposalID: proposal.ID, ActionID: actionID, EffectType: effectType, EntityType: entityType, EntityID: entityID, EventSequence: eventSequence, CreatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO manager_proposal_effects(id,workspace_id,project_id,objective_id,proposal_id,action_id,effect_type,entity_type,entity_id,event_sequence,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			effect.ID, effect.WorkspaceID, effect.ProjectID, effect.ObjectiveID, effect.ProposalID, effect.ActionID, effect.EffectType, effect.EntityType, effect.EntityID, effect.EventSequence, effect.CreatedAt); err != nil {
			return storageFailure("insert manager proposal effect", err)
		}
		effects = append(effects, effect)
		return nil
	}
	resolveRef := func(ref domain.ProposalTaskRef) (domain.Task, error) {
		if ref.ProposalTaskKey != "" {
			task, exists := createdTasks[ref.ProposalTaskKey]
			if !exists {
				return domain.Task{}, &Error{Code: CodeManagerProposalConflict, Message: "proposal task reference is not available"}
			}
			return task, nil
		}
		return queryTask(ctx, tx, proposal.WorkspaceID, ref.TaskID)
	}
	createTask := func(action domain.ManagerProposalAction, value domain.ProposalCreateTaskAction) (domain.Task, error) {
		id, err := randomID("task_")
		if err != nil {
			return domain.Task{}, storageFailure("generate accepted task id", err)
		}
		task := domain.Task{ID: id, WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID, ObjectiveID: proposal.ObjectiveID,
			Title: strings.TrimSpace(value.Title), Description: strings.TrimSpace(value.Description), Status: domain.TaskReady,
			Priority: value.Priority, Budget: value.Budget, Revision: 1, CreatedAt: now, UpdatedAt: now,
			CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,workspace_id,project_id,objective_id,title,description,status,priority,budget_tokens,budget_cost_cents,budget_time_seconds,revision,created_at,updated_at,created_by,updated_by) VALUES (?,?,?,?,?,NULLIF(?,''),'ready',?,?,?,?,1,?,?,?,?)`,
			task.ID, task.WorkspaceID, task.ProjectID, task.ObjectiveID, task.Title, task.Description, task.Priority,
			task.Budget.TokenLimit, task.Budget.CostCents, task.Budget.TimeSeconds, now, now, localOwnerActorID, localOwnerActorID); err != nil {
			return domain.Task{}, storageFailure("insert accepted manager task", err)
		}
		sequence, err := appendEvent(ctx, tx, proposal.WorkspaceID, "task", task.ID, 1, taskCreated, correlationID, now, map[string]any{
			"project_id": task.ProjectID, "objective_id": task.ObjectiveID, "title": task.Title, "priority": task.Priority, "budget": task.Budget, "source_proposal_id": proposal.ID,
		})
		if err != nil {
			return domain.Task{}, err
		}
		if err := addEffect(action.ID, "created", "task", task.ID, sequence); err != nil {
			return domain.Task{}, err
		}
		createdTasks[value.TaskKey] = task
		return task, nil
	}
	createIntent := func(action domain.ManagerProposalAction, task domain.Task, profileID string) error {
		profile, err := queryLaunchProfile(ctx, tx, proposal.WorkspaceID, profileID)
		if err != nil {
			return err
		}
		id, err := randomID("sintent_")
		if err != nil {
			return storageFailure("generate scheduling intent id", err)
		}
		intent := domain.SchedulingIntent{ID: id, WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID, ObjectiveID: proposal.ObjectiveID,
			TaskID: task.ID, AgentID: profile.AgentID, LaunchProfileID: profile.ID, SourceProposalID: proposal.ID, SourceActionID: action.ID,
			Status: domain.SchedulingIntentPending, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
		if _, err := tx.ExecContext(ctx, `INSERT INTO scheduling_intents(id,workspace_id,project_id,objective_id,task_id,agent_id,launch_profile_id,source_proposal_id,source_action_id,status,attempts,revision,created_at,updated_at,created_by,updated_by) VALUES (?,?,?,?,?,?,?,?,?,'pending',0,1,?,?,?,?)`,
			intent.ID, intent.WorkspaceID, intent.ProjectID, intent.ObjectiveID, intent.TaskID, intent.AgentID, intent.LaunchProfileID,
			intent.SourceProposalID, intent.SourceActionID, now, now, localOwnerActorID, localOwnerActorID); err != nil {
			return storageFailure("insert scheduling intent", err)
		}
		sequence, err := appendEvent(ctx, tx, proposal.WorkspaceID, "scheduling_intent", intent.ID, 1, schedulingIntentCreatedEvent, correlationID, now, map[string]any{
			"task_id": intent.TaskID, "agent_id": intent.AgentID, "launch_profile_id": intent.LaunchProfileID, "source_proposal_id": proposal.ID,
		})
		if err != nil {
			return err
		}
		return addEffect(action.ID, "created", "scheduling_intent", intent.ID, sequence)
	}
	// Materialize all proposal-created tasks first so later dependency and claim
	// actions can cite a stable task_key irrespective of their position.
	for _, action := range proposal.Actions {
		if action.Type == domain.ProposalActionCreateTask {
			task, err := createTask(action, *action.CreateTask)
			if err != nil {
				return nil, err
			}
			if err := createIntent(action, task, action.CreateTask.LaunchProfileID); err != nil {
				return nil, err
			}
		}
	}
	for _, action := range proposal.Actions {
		switch action.Type {
		case domain.ProposalActionCreateTask:
			continue
		case domain.ProposalActionAddDependency:
			task, err := resolveRef(action.AddDependency.Task)
			if err != nil {
				return nil, err
			}
			dependsOn, err := resolveRef(action.AddDependency.DependsOn)
			if err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO task_dependencies(task_id,depends_on_task_id,created_at,created_by) VALUES (?,?,?,?)", task.ID, dependsOn.ID, now, localOwnerActorID); err != nil {
				return nil, storageFailure("insert accepted task dependency", err)
			}
			task.Revision++
			if _, err := tx.ExecContext(ctx, "UPDATE tasks SET revision=?,updated_at=?,updated_by=? WHERE id=?", task.Revision, now, localOwnerActorID, task.ID); err != nil {
				return nil, storageFailure("revise dependent task", err)
			}
			sequence, err := appendEvent(ctx, tx, proposal.WorkspaceID, "task", task.ID, task.Revision, taskDependencyAdded, correlationID, now, map[string]any{"depends_on_task_id": dependsOn.ID, "source_proposal_id": proposal.ID})
			if err != nil {
				return nil, err
			}
			if err := addEffect(action.ID, "dependency_added", "task", task.ID, sequence); err != nil {
				return nil, err
			}
			if action.AddDependency.Task.ProposalTaskKey != "" {
				createdTasks[action.AddDependency.Task.ProposalTaskKey] = task
			}
		case domain.ProposalActionDeclareClaimRequirement:
			task, err := resolveRef(action.DeclareClaimRequirement.Task)
			if err != nil {
				return nil, err
			}
			id, err := randomID("claimr_")
			if err != nil {
				return nil, storageFailure("generate claim requirement id", err)
			}
			value := action.DeclareClaimRequirement
			if _, err := tx.ExecContext(ctx, `INSERT INTO task_claim_requirements(id,workspace_id,project_id,objective_id,task_id,source_proposal_id,source_action_id,kind,target,mode,conflict_policy,revision,created_at,updated_at,created_by,updated_by) VALUES (?,?,?,?,?,?,?,?,?,?,?,1,?,?,?,?)`,
				id, proposal.WorkspaceID, proposal.ProjectID, proposal.ObjectiveID, task.ID, proposal.ID, action.ID, value.Kind, value.Target, value.Mode, value.ConflictPolicy, now, now, localOwnerActorID, localOwnerActorID); err != nil {
				return nil, storageFailure("insert task claim requirement", err)
			}
			sequence, err := appendEvent(ctx, tx, proposal.WorkspaceID, "task_claim_requirement", id, 1, taskClaimRequirementCreatedEvent, correlationID, now, map[string]any{"task_id": task.ID, "kind": value.Kind, "target": value.Target})
			if err != nil {
				return nil, err
			}
			if err := addEffect(action.ID, "created", "task_claim_requirement", id, sequence); err != nil {
				return nil, err
			}
		case domain.ProposalActionAssignTask:
			task, err := resolveRef(action.AssignTask.Task)
			if err != nil {
				return nil, err
			}
			if err := createIntent(action, task, action.AssignTask.LaunchProfileID); err != nil {
				return nil, err
			}
		case domain.ProposalActionRequestReview:
			target, err := resolveRef(action.RequestReview.Task)
			if err != nil {
				return nil, err
			}
			created, err := createTask(action, domain.ProposalCreateTaskAction{TaskKey: "review-" + action.ID, LaunchProfileID: action.RequestReview.LaunchProfileID,
				Title: action.RequestReview.Title, Description: action.RequestReview.Description, Priority: action.RequestReview.Priority, Budget: action.RequestReview.Budget})
			if err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO task_dependencies(task_id,depends_on_task_id,created_at,created_by) VALUES (?,?,?,?)", created.ID, target.ID, now, localOwnerActorID); err != nil {
				return nil, storageFailure("insert review dependency", err)
			}
			created.Revision++
			if _, err := tx.ExecContext(ctx, "UPDATE tasks SET revision=?,updated_at=?,updated_by=? WHERE id=?", created.Revision, now, localOwnerActorID, created.ID); err != nil {
				return nil, storageFailure("revise review task", err)
			}
			sequence, err := appendEvent(ctx, tx, proposal.WorkspaceID, "task", created.ID, created.Revision, taskDependencyAdded, correlationID, now, map[string]any{
				"depends_on_task_id": target.ID, "source_proposal_id": proposal.ID,
			})
			if err != nil {
				return nil, err
			}
			if err := addEffect(action.ID, "dependency_added", "task", created.ID, sequence); err != nil {
				return nil, err
			}
			if err := createIntent(action, created, action.RequestReview.LaunchProfileID); err != nil {
				return nil, err
			}
		case domain.ProposalActionRequestAction:
			escalationEffects, err := s.materializeAcceptedManagerEscalation(ctx, tx, proposal, action, correlationID, now)
			if err != nil {
				return nil, err
			}
			for _, effect := range escalationEffects {
				if err := addEffect(action.ID, effect.effectType, effect.entityType, effect.entityID, effect.eventSequence); err != nil {
					return nil, err
				}
			}
		}
	}
	return effects, nil
}

type managerEscalationEffect struct {
	effectType    string
	entityType    string
	entityID      string
	eventSequence int64
}

// materializeAcceptedManagerEscalation converts an accepted, still-inert
// request_action into one exact owner decision. It does not perform the target
// mutation; allow/deny remains a separately idempotent owner boundary.
func (s *Store) materializeAcceptedManagerEscalation(ctx context.Context, tx *sql.Tx, proposal domain.ManagerProposal, source domain.ManagerProposalAction, correlationID, now string) ([]managerEscalationEffect, error) {
	request := *source.RequestAction
	policy, err := querySupervisorPolicy(ctx, tx, proposal.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var task domain.Task
	var targetRun *domain.Run
	constraints := map[string]any{
		"source_proposal_id": proposal.ID, "source_action_id": source.ID,
		"requested_response": request.Response, "expected_revision": request.ExpectedRevision,
	}
	if request.TargetRunID != "" {
		run, err := queryRun(ctx, tx, proposal.WorkspaceID, request.TargetRunID)
		if err != nil {
			return nil, err
		}
		task, err = queryTask(ctx, tx, proposal.WorkspaceID, run.TaskID)
		if err != nil {
			return nil, err
		}
		targetRun = &run
	} else {
		task, err = queryTask(ctx, tx, proposal.WorkspaceID, request.TargetTaskID)
		if err != nil {
			return nil, err
		}
		if request.Response == domain.ProposalResponseRetryTask {
			prior, err := queryLatestDefiniteStartFailure(ctx, tx, proposal.WorkspaceID, task.ID)
			if err != nil {
				return nil, err
			}
			targetRun = &prior
			constraints["prior_run_id"] = prior.ID
			constraints["prior_run_revision"] = prior.Revision
		}
	}
	if request.LaunchProfileID != "" {
		profile, err := queryLaunchProfile(ctx, tx, proposal.WorkspaceID, request.LaunchProfileID)
		if err != nil {
			return nil, err
		}
		constraints["launch_profile_id"] = profile.ID
		constraints["launch_profile_revision"] = profile.Revision
	}
	action, err := newSupervisorAction(policy, proposal.AsOfEventSequence, domain.SupervisorConditionManagerEscalation,
		request.Response, domain.SupervisorActionAwaitingApproval, task, targetRun, nil,
		[]string{request.Reason, "accepted manager escalation remains inert until a separate explicit owner approval"}, constraints, now)
	if err != nil {
		return nil, storageFailure("build accepted manager escalation", err)
	}
	action.SourceProposalID, action.SourceActionID = proposal.ID, source.ID
	if request.Response == domain.ProposalResponseRetryTask {
		action.PriorRunID, action.RunID, action.EntityRevision = targetRun.ID, "", task.Revision
	}
	approvalID, err := randomID("appr_")
	if err != nil {
		return nil, storageFailure("generate escalation approval", err)
	}
	action.ApprovalID = approvalID
	action.ConditionKey = supervisorConditionKeyForAction(action)
	_, action.ContentSHA256, err = canonicalContent(supervisorActionContent{ConditionKey: action.ConditionKey, Condition: action.Condition,
		Response: action.Response, EntityRevision: action.EntityRevision, PolicyRevision: action.PolicyRevision,
		AsOfEventSequence: action.AsOfEventSequence, Reasons: action.Reasons, Constraints: action.ConstraintSnapshot})
	if err != nil {
		return nil, storageFailure("seal accepted manager escalation", err)
	}
	if err := insertSupervisorAction(ctx, tx, action); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO approval_requests(id,workspace_id,action_id,status,expected_action_revision,revision,created_at,updated_at,created_by,updated_by) VALUES (?,?,?,'pending',1,1,?,?,?,?)`,
		approvalID, proposal.WorkspaceID, action.ID, now, now, "subsystem:supervisor", "subsystem:supervisor"); err != nil {
		return nil, storageFailure("insert escalation approval", err)
	}
	actionSequence, err := appendEvent(ctx, tx, proposal.WorkspaceID, "supervisor_action", action.ID, 1, supervisorActionRecordedEvent, correlationID, now, map[string]any{
		"condition": action.Condition, "response": action.Response, "approval_id": approvalID,
		"source_proposal_id": proposal.ID, "source_action_id": source.ID,
	})
	if err != nil {
		return nil, err
	}
	if err := s.sealSupervisorAction(ctx, tx, action, actionSequence); err != nil {
		return nil, err
	}
	approvalSequence, err := appendEvent(ctx, tx, proposal.WorkspaceID, "approval_request", approvalID, 1, approvalRequestedEvent, correlationID, now, map[string]any{
		"action_id": action.ID, "source_proposal_id": proposal.ID,
	})
	if err != nil {
		return nil, err
	}
	return []managerEscalationEffect{
		{effectType: "created", entityType: "supervisor_action", entityID: action.ID, eventSequence: actionSequence},
		{effectType: "created", entityType: "approval_request", entityID: approvalID, eventSequence: approvalSequence},
	}, nil
}

func queryLatestDefiniteStartFailure(ctx context.Context, database queryRower, workspaceID, taskID string) (domain.Run, error) {
	var run domain.Run
	err := scanRun(database.QueryRowContext(ctx, runSelect+` WHERE r.workspace_id=? AND r.task_id=? AND r.status='start_failed' AND r.step_cursor=0 ORDER BY r.created_at DESC,r.id DESC LIMIT 1`, workspaceID, taskID), &run)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Run{}, &Error{Code: CodeManagerProposalConflict, Message: "task has no definite start failure to retry"}
	}
	if err != nil {
		return domain.Run{}, storageFailure("query definite start failure", err)
	}
	return run, nil
}

func (s *Store) ManagerProposals(ctx context.Context, query ListManagerProposalsQuery) ([]domain.ManagerProposal, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return nil, err
	}
	statement := managerProposalSelect + ` WHERE workspace_id=? AND EXISTS (
  SELECT 1 FROM manager_proposal_submissions receipt WHERE receipt.proposal_id=manager_proposals.id
) AND (status IN ('pending','invalid') OR EXISTS (
  SELECT 1 FROM manager_proposal_decisions decision WHERE decision.proposal_id=manager_proposals.id AND decision.status=manager_proposals.status
    AND decision.effect_count=(SELECT COUNT(*) FROM manager_proposal_effects effect WHERE effect.proposal_id=manager_proposals.id)
    AND decision.effects_sha256=lower(hex(sha256(CAST(COALESCE((
      SELECT json_group_array(json_object('action_id',action_id,'effect_type',effect_type,'entity_type',entity_type,'entity_id',entity_id,'event_sequence',event_sequence))
      FROM (SELECT * FROM manager_proposal_effects WHERE proposal_id=manager_proposals.id ORDER BY action_id,effect_type,entity_type,entity_id)
    ),'[]') AS BLOB))))
))`
	arguments := []any{workspace.ID}
	filters := []struct{ clause, value string }{
		{"project_id=?", strings.TrimSpace(query.ProjectIdentifier)}, {"objective_id=?", strings.TrimSpace(query.ObjectiveID)},
		{"source_run_id=?", strings.TrimSpace(query.SourceRunID)}, {"grant_id=?", strings.TrimSpace(query.ManagerGrantID)},
		{"kind=?", strings.TrimSpace(query.Kind)}, {"status=?", strings.TrimSpace(query.Status)},
	}
	for _, filter := range filters {
		if filter.value != "" {
			statement += " AND " + filter.clause
			arguments = append(arguments, filter.value)
		}
	}
	statement += " ORDER BY created_at,id LIMIT ?"
	arguments = append(arguments, boundedManagementLimit(query.Limit))
	rows, err := s.db.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, storageFailure("list manager proposals", err)
	}
	defer rows.Close()
	result := make([]domain.ManagerProposal, 0)
	for rows.Next() {
		proposal, err := scanManagerProposal(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, proposal)
	}
	return result, rows.Err()
}

func (s *Store) ConfigureSupervisorPolicy(ctx context.Context, command ConfigureSupervisorPolicyCommand) (MutationResult[domain.SupervisorPolicy], error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.ExpectedRevision < 1 || !validSupervisorLimits(command.Limits) ||
		command.AutoRetryLimit < 0 || command.AutoRetryLimit > 3 || command.RetryCooldownSeconds < 0 || command.RetryCooldownSeconds > 86400 ||
		command.AutoRetryLimit > 0 && command.RetryCooldownSeconds == 0 {
		return MutationResult[domain.SupervisorPolicy]{}, &Error{Code: CodeInvalidSupervisorPolicy, Message: "supervisor policy requires expected revision, bounded concurrency, retry limit 0..3, and retry cooldown"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidSupervisorPolicy); err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, err
	}
	projectLimits, err := canonicalConcurrencyMap(command.Limits.ProjectConcurrency, true)
	if err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, err
	}
	providerLimits, err := canonicalConcurrencyMap(command.Limits.ProviderConcurrency, false)
	if err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, err
	}
	command.Limits.ProjectConcurrency, command.Limits.ProviderConcurrency = projectLimits, providerLimits
	requestHash, err := hashManagementCommand("supervisor.policy.configure", command)
	if err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, storageFailure("hash supervisor policy", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, storageFailure("begin supervisor policy configuration", err)
	}
	defer tx.Rollback()
	var replay MutationResult[domain.SupervisorPolicy]
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "supervisor.policy.configure", requestHash, &replay); err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, err
	}
	current, err := querySupervisorPolicy(ctx, tx, workspace.ID)
	if err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, err
	}
	if current.Revision != command.ExpectedRevision {
		return MutationResult[domain.SupervisorPolicy]{}, revisionConflict("supervisor policy", workspace.ID, command.ExpectedRevision, current.Revision)
	}
	revision := current.Revision + 1
	for projectID, maximum := range projectLimits {
		if _, err := queryProject(ctx, tx, workspace.ID, projectID); err != nil {
			return MutationResult[domain.SupervisorPolicy]{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO supervisor_policy_project_limits(workspace_id,policy_revision,project_id,max_concurrency) VALUES (?,?,?,?)`, workspace.ID, revision, projectID, maximum); err != nil {
			return MutationResult[domain.SupervisorPolicy]{}, storageFailure("insert supervisor project limit", err)
		}
	}
	for provider, maximum := range providerLimits {
		if _, err := tx.ExecContext(ctx, `INSERT INTO supervisor_policy_provider_limits(workspace_id,policy_revision,provider,max_concurrency) VALUES (?,?,?,?)`, workspace.ID, revision, provider, maximum); err != nil {
			return MutationResult[domain.SupervisorPolicy]{}, storageFailure("insert supervisor provider limit", err)
		}
	}
	projectJSON, _ := json.Marshal(projectLimits)
	providerJSON, _ := json.Marshal(providerLimits)
	now := s.nowText()
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "supervisor_policy", workspace.ID, revision, "supervisor.policy_configured", command.CorrelationID, now, map[string]any{
		"enabled": command.Enabled, "limits": command.Limits, "auto_schedule": command.AutoSchedule,
		"auto_retry_limit": command.AutoRetryLimit, "retry_cooldown_seconds": command.RetryCooldownSeconds,
	})
	if err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO supervisor_policies(workspace_id,revision,enabled,max_active_runs,max_starting_runs,default_project_concurrency,default_provider_concurrency,project_concurrency_json,provider_concurrency_json,auto_schedule,auto_retry_limit,retry_cooldown_seconds,event_sequence,created_at,updated_at,created_by,updated_by) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		workspace.ID, revision, command.Enabled, command.Limits.MaxActiveRuns, command.Limits.MaxStartingRuns,
		command.Limits.DefaultProjectConcurrency, command.Limits.DefaultProviderConcurrency, string(projectJSON), string(providerJSON),
		command.AutoSchedule, command.AutoRetryLimit, command.RetryCooldownSeconds, sequence, now, now, localOwnerActorID, localOwnerActorID); err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, storageFailure("insert supervisor policy revision", err)
	}
	policy := domain.SupervisorPolicy{WorkspaceID: workspace.ID, Enabled: command.Enabled, Limits: command.Limits, AutoSchedule: command.AutoSchedule,
		AutoRetryLimit: command.AutoRetryLimit, RetryCooldownSeconds: command.RetryCooldownSeconds, Revision: revision,
		CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	// A definite start failure intentionally leaves its scheduling intent open
	// while a bounded retry remains authorized. Policy changes are the other
	// side of that invariant: disabling retry (or lowering the bound below the
	// attempts already consumed) must close the intent in this same owner
	// transaction, because a disabled policy is no longer scanned by the daemon.
	if err := terminalizeStrandedRetryIntentsForPolicy(ctx, tx, policy, command.CorrelationID, now); err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, err
	}
	result := MutationResult[domain.SupervisorPolicy]{Value: policy, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, err
	}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "supervisor.policy.configure", requestHash, result, now); err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, err
	}
	if err := tx.Commit(); err != nil {
		return MutationResult[domain.SupervisorPolicy]{}, storageFailure("commit supervisor policy", err)
	}
	return result, nil
}

func (s *Store) SupervisorPolicy(ctx context.Context, workspaceIdentifier string) (domain.SupervisorPolicy, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.SupervisorPolicy{}, err
	}
	return querySupervisorPolicy(ctx, s.db, workspace.ID)
}

func (s *Store) SupervisorWorkspaceIDs(ctx context.Context, after string, limit int) ([]string, error) {
	after = strings.TrimSpace(after)
	if limit < 1 || limit > 100 {
		return nil, &Error{Code: CodeInvalidSupervisorPolicy, Message: "supervisor workspace page limit must be from 1 to 100"}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT policy.workspace_id FROM supervisor_policies policy
WHERE policy.enabled=1
  AND policy.revision=(SELECT MAX(latest.revision) FROM supervisor_policies latest WHERE latest.workspace_id=policy.workspace_id)
  AND policy.workspace_id>?
ORDER BY policy.workspace_id LIMIT ?`, after, limit)
	if err != nil {
		return nil, storageFailure("list enabled supervisor workspaces", err)
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, storageFailure("scan enabled supervisor workspace", err)
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (s *Store) RunSupervisor(ctx context.Context, command RunSupervisorCommand) (SupervisorRunResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.Limit < 0 || command.Limit > 100 {
		return SupervisorRunResult{}, &Error{Code: CodeInvalidSupervisorPolicy, Message: "supervisor run requires workspace and a limit from 0 to 100"}
	}
	if command.Limit == 0 {
		command.Limit = 100
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidSupervisorPolicy); err != nil {
		return SupervisorRunResult{}, err
	}
	requestHash, err := hashManagementCommand("supervisor.run", command)
	if err != nil {
		return SupervisorRunResult{}, storageFailure("hash supervisor run", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SupervisorRunResult{}, storageFailure("begin supervisor run", err)
	}
	defer tx.Rollback()
	var replay SupervisorRunResult
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "supervisor.run", requestHash, &replay); err != nil {
		return SupervisorRunResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return SupervisorRunResult{}, err
	}
	policy, err := querySupervisorPolicy(ctx, tx, workspace.ID)
	if err != nil {
		return SupervisorRunResult{}, err
	}
	if !policy.Enabled {
		return SupervisorRunResult{}, &Error{Code: CodeInvalidSupervisorPolicy, Message: "supervisor policy is disabled"}
	}
	now := s.nowText()
	journal, err := inspectSupervisorJournal(ctx, tx, workspace.ID)
	if err != nil {
		return SupervisorRunResult{}, err
	}
	cursor := journal.Cutoff
	if !journal.CaughtUp {
		if err := advanceSupervisorJournalCursor(ctx, tx, workspace.ID, journal.Through, now); err != nil {
			return SupervisorRunResult{}, err
		}
		result := SupervisorRunResult{Policy: policy, Actions: []domain.SupervisorAction{}, ScheduledRunIDs: []string{}, EventSequence: journal.Through}
		if command.PersistNoop {
			if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "supervisor.run", requestHash, result, now); err != nil {
				return SupervisorRunResult{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return SupervisorRunResult{}, storageFailure("commit supervisor journal catch-up", err)
		}
		return result, nil
	}
	// Expiry is reconciled in the same serialized transaction and only after
	// the journal is understood through the captured cutoff. Reserved and lost
	// runs remain protected by the reconciliation predicates and schema guards.
	expiredAssignments, err := expireAssignmentsInTransaction(ctx, tx, workspace.ID, s.clock().UTC(), derivedCorrelationID(command.CorrelationID, "assignments"))
	if err != nil {
		return SupervisorRunResult{}, err
	}
	expiredClaims, err := expireClaimsInTransaction(ctx, tx, workspace.ID, now, derivedCorrelationID(command.CorrelationID, "claims"))
	if err != nil {
		return SupervisorRunResult{}, err
	}
	// Lease reconciliation can append known facts after the captured journal
	// cutoff. They are part of this serialized decision, so include them in the
	// per-intent evaluation watermark and action evidence.
	if expiredAssignments > 0 || expiredClaims > 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM events WHERE workspace_id=?`, workspace.ID).Scan(&cursor); err != nil {
			return SupervisorRunResult{}, storageFailure("read post-reconciliation supervisor cursor", err)
		}
	}
	actions := make([]domain.SupervisorAction, 0, command.Limit)
	scheduled := make([]string, 0)
	materialChanged := false
	if policy.AutoRetryLimit > 0 {
		before := len(actions)
		if err := s.retryStartFailedRuns(ctx, tx, policy, cursor, command.Limit, &actions, &scheduled, command.CorrelationID, now); err != nil {
			return SupervisorRunResult{}, err
		}
		materialChanged = len(actions) != before
	}
	if policy.AutoSchedule {
		intentRows, err := tx.QueryContext(ctx, `SELECT intent.id FROM scheduling_intents intent JOIN tasks task ON task.id=intent.task_id
WHERE intent.workspace_id=? AND intent.status IN ('pending','deferred')
  AND task.status='ready'
  AND NOT EXISTS (SELECT 1 FROM task_assignments assignment WHERE assignment.task_id=task.id AND assignment.status='active')
  AND (intent.next_attempt_at IS NULL OR crewfold_timestamp_key(intent.next_attempt_at)<=crewfold_timestamp_key(?)
    OR `+schedulingIntentRelevantWakeSQL+`)
  AND NOT EXISTS (
    SELECT 1 FROM task_dependencies dependency JOIN tasks upstream ON upstream.id=dependency.depends_on_task_id
    WHERE dependency.task_id=task.id AND upstream.status<>'completed'
  )
ORDER BY task.priority DESC, `+schedulingIntentEligibilityKeySQL+`,task.id,intent.id
LIMIT ?`, workspace.ID, now, cursor, maximumSupervisorQueueCandidates)
		if err != nil {
			return SupervisorRunResult{}, storageFailure("scan scheduling intents", err)
		}
		intentIDs := make([]string, 0)
		for intentRows.Next() {
			var id string
			if err := intentRows.Scan(&id); err != nil {
				intentRows.Close()
				return SupervisorRunResult{}, storageFailure("scan scheduling intent", err)
			}
			intentIDs = append(intentIDs, id)
		}
		if err := intentRows.Close(); err != nil {
			return SupervisorRunResult{}, err
		}
		for _, intentID := range intentIDs {
			if len(actions) >= command.Limit {
				break
			}
			intent, err := querySchedulingIntent(ctx, tx, workspace.ID, intentID)
			if err != nil {
				return SupervisorRunResult{}, err
			}
			task, err := queryTask(ctx, tx, workspace.ID, intent.TaskID)
			if err != nil {
				return SupervisorRunResult{}, err
			}
			readiness, err := taskReadiness(ctx, tx, task)
			if err != nil {
				return SupervisorRunResult{}, err
			}
			if !readiness.Ready {
				continue
			}
			profile, err := queryLaunchProfile(ctx, tx, workspace.ID, intent.LaunchProfileID)
			if err != nil {
				if ErrorCode(err) != CodeLaunchProfileNotFound {
					return SupervisorRunResult{}, err
				}
				// Preserve the exact requested identity while the shared placement
				// preflight records every profile-independent fact and seals the
				// missing-profile failure as one bounded deferred action.
				profile = domain.LaunchProfile{ID: intent.LaunchProfileID, WorkspaceID: intent.WorkspaceID,
					ProjectID: intent.ProjectID, AgentID: intent.AgentID}
			}
			action, err := newSupervisorAction(policy, cursor, domain.SupervisorConditionDependencyReady, domain.SupervisorResponseSchedule, domain.SupervisorActionApplied,
				task, nil, &intent, []string{"task dependencies are complete", "exact launch profile and agent revision are active"},
				map[string]any{"policy_revision": policy.Revision, "launch_profile_id": profile.ID}, now)
			if err != nil {
				return SupervisorRunResult{}, storageFailure("build scheduling action", err)
			}
			action.AppliedAt = now
			runID, err := s.scheduleIntentInTransaction(ctx, tx, policy, intent, task, profile, &action, command.CorrelationID, now)
			if err != nil {
				if isSupervisorCapacityError(err) {
					reason := err.Error()
					observedIntent := intent
					changed, deferErr := deferSchedulingIntent(ctx, tx, &intent, reason, cursor, now)
					if deferErr != nil {
						return SupervisorRunResult{}, deferErr
					}
					if changed {
						deferred, deferErr := s.recordDeferredSchedulingAction(ctx, tx, policy, cursor, task, observedIntent, reason,
							&profile, supervisorDeferralEvidence(err), command.CorrelationID, now)
						if deferErr != nil {
							return SupervisorRunResult{}, deferErr
						}
						actions = append(actions, deferred)
					}
					materialChanged = materialChanged || changed
					continue
				}
				return SupervisorRunResult{}, err
			}
			actions, scheduled = append(actions, action), append(scheduled, runID)
			materialChanged = true
		}
	}
	if len(actions) < command.Limit {
		blockedRows, err := tx.QueryContext(ctx, `SELECT run.id FROM runs run
WHERE run.workspace_id=? AND run.status='blocked'
  AND NOT EXISTS (
    SELECT 1 FROM supervisor_actions action
    JOIN supervisor_action_receipts receipt ON receipt.action_id=action.id
    WHERE action.workspace_id=run.workspace_id AND action.run_id=run.id
      AND action.condition='blocked' AND action.response='resume_run'
      AND action.entity_revision=run.revision AND receipt.condition_key=action.condition_key
  )
ORDER BY run.updated_at,run.id LIMIT ?`, workspace.ID, command.Limit-len(actions))
		if err != nil {
			return SupervisorRunResult{}, storageFailure("scan blocked runs", err)
		}
		runIDs := make([]string, 0)
		for blockedRows.Next() {
			var id string
			if err := blockedRows.Scan(&id); err != nil {
				blockedRows.Close()
				return SupervisorRunResult{}, err
			}
			runIDs = append(runIDs, id)
		}
		blockedRows.Close()
		for _, runID := range runIDs {
			if len(actions) >= command.Limit {
				break
			}
			run, err := queryRun(ctx, tx, workspace.ID, runID)
			if err != nil {
				return SupervisorRunResult{}, err
			}
			conditionKey := supervisorConditionKey(domain.SupervisorConditionBlocked, domain.SupervisorResponseResumeRun, "", run.TaskID, run.ID, run.Revision)
			var exists int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM supervisor_action_receipts WHERE condition_key=?`, conditionKey).Scan(&exists); err != nil {
				return SupervisorRunResult{}, storageFailure("dedupe blocked action", err)
			}
			if exists != 0 {
				continue
			}
			task, err := queryTask(ctx, tx, workspace.ID, run.TaskID)
			if err != nil {
				return SupervisorRunResult{}, err
			}
			action, err := newSupervisorAction(policy, cursor, domain.SupervisorConditionBlocked, domain.SupervisorResponseResumeRun, domain.SupervisorActionAwaitingApproval,
				task, &run, nil, []string{"run is durably blocked", "resuming requires explicit owner approval"}, map[string]any{"blocked_question": run.BlockedQuestion}, now)
			if err != nil {
				return SupervisorRunResult{}, err
			}
			approvalID, err := randomID("appr_")
			if err != nil {
				return SupervisorRunResult{}, storageFailure("generate approval id", err)
			}
			action.ApprovalID = approvalID
			if err := insertSupervisorAction(ctx, tx, action); err != nil {
				return SupervisorRunResult{}, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO approval_requests(id,workspace_id,action_id,status,expected_action_revision,revision,created_at,updated_at,created_by,updated_by) VALUES (?,?,?,'pending',1,1,?,?,?,?)`, approvalID, workspace.ID, action.ID, now, now, "subsystem:supervisor", "subsystem:supervisor"); err != nil {
				return SupervisorRunResult{}, storageFailure("insert approval request", err)
			}
			actionSequence, err := appendEventForActor(ctx, tx, workspace.ID, "supervisor_action", action.ID, 1, supervisorActionRecordedEvent, command.CorrelationID, now, "subsystem:supervisor", "subsystem", map[string]any{"condition": action.Condition, "response": action.Response, "approval_id": approvalID})
			if err != nil {
				return SupervisorRunResult{}, err
			}
			if err := s.sealSupervisorAction(ctx, tx, action, actionSequence); err != nil {
				return SupervisorRunResult{}, err
			}
			if _, err := appendEventForActor(ctx, tx, workspace.ID, "approval_request", approvalID, 1, approvalRequestedEvent, command.CorrelationID, now, "subsystem:supervisor", "subsystem", map[string]any{"action_id": action.ID}); err != nil {
				return SupervisorRunResult{}, err
			}
			actions = append(actions, action)
			materialChanged = true
		}
	}
	actionsBeforeNonAutomatic := len(actions)
	if err := s.materializeNonAutomaticSupervisorConditions(ctx, tx, policy, cursor, command.Limit-len(actions), &actions, command.CorrelationID, now); err != nil {
		return SupervisorRunResult{}, err
	}
	materialChanged = materialChanged || len(actions) != actionsBeforeNonAutomatic
	if !materialChanged {
		// No scan event is emitted for an unchanged projection. New, classified
		// facts still advance the durable authority watermark. Public calls also
		// retain their successful no-op result, so the same key can never acquire
		// new effects on a later retry. Daemon passes use one-shot keys and remain
		// event-idle once the watermark is caught up.
		if journal.Through > journal.From {
			if err := advanceSupervisorJournalCursor(ctx, tx, workspace.ID, journal.Through, now); err != nil {
				return SupervisorRunResult{}, err
			}
		}
		result := SupervisorRunResult{Policy: policy, Actions: actions, ScheduledRunIDs: scheduled, EventSequence: cursor}
		if command.PersistNoop {
			if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "supervisor.run", requestHash, result, now); err != nil {
				return SupervisorRunResult{}, err
			}
		}
		if err := tx.Commit(); err != nil {
			return SupervisorRunResult{}, storageFailure("commit idle supervisor scan", err)
		}
		return result, nil
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return SupervisorRunResult{}, err
	}
	scanSequence, err := appendEventForActor(ctx, tx, workspace.ID, "supervisor_state", workspace.ID, policy.Revision, supervisorScanCompletedEvent, command.CorrelationID, now, "subsystem:supervisor", "subsystem", map[string]any{"policy_revision": policy.Revision, "action_count": len(actions), "scheduled_run_ids": scheduled, "as_of_event_sequence": cursor})
	if err != nil {
		return SupervisorRunResult{}, err
	}
	if err := advanceSupervisorJournalCursor(ctx, tx, workspace.ID, scanSequence, now); err != nil {
		return SupervisorRunResult{}, err
	}
	result := SupervisorRunResult{Policy: policy, Actions: actions, ScheduledRunIDs: scheduled, EventSequence: scanSequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return SupervisorRunResult{}, err
	}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "supervisor.run", requestHash, result, now); err != nil {
		return SupervisorRunResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SupervisorRunResult{}, storageFailure("commit supervisor run", err)
	}
	return result, nil
}

func isSupervisorCapacityError(err error) bool {
	var typed *Error
	return errors.As(err, &typed) && (typed.Code == CodePlacementUnavailable || typed.Code == CodeSchedulingPaused || typed.Code == CodeClaimConflict)
}

type supervisorDeferralError struct {
	cause    *Error
	evidence map[string]any
}

func (e *supervisorDeferralError) Error() string { return e.cause.Error() }
func (e *supervisorDeferralError) Unwrap() error { return e.cause }

func newSupervisorDeferral(code, message string, evidence map[string]any) error {
	return &supervisorDeferralError{cause: &Error{Code: code, Message: message}, evidence: evidence}
}

func supervisorDeferralEvidence(err error) map[string]any {
	var typed *supervisorDeferralError
	if errors.As(err, &typed) {
		result := make(map[string]any, len(typed.evidence))
		for key, value := range typed.evidence {
			result[key] = value
		}
		return result
	}
	return map[string]any{"dimension": "unknown"}
}

type supervisorRetryAuthority struct {
	profile    domain.LaunchProfile
	intentID   string
	retryCount int
}

// retryStartFailedRuns applies only definite, supervisor-originated start
// failures. It creates a fresh run and capability while retaining the exact
// assignment, claims, and immutable launch profile; it never searches for
// another agent or profile.
func (s *Store) retryStartFailedRuns(ctx context.Context, tx *sql.Tx, policy domain.SupervisorPolicy, cursor int64, limit int, actions *[]domain.SupervisorAction, scheduled *[]string, correlationID, now string) error {
	// The SQL prefilter mirrors the final authority/capacity checks closely so
	// an old ineligible failure cannot occupy every bounded page forever. The Go
	// helper still revalidates the selected rows before any effect.
	rows, err := tx.QueryContext(ctx, `WITH retry_source AS (
  SELECT run_id,launch_profile_id,launch_profile_revision,assignment_id,intent_id,0 AS attempt
  FROM run_scheduling_receipts
  UNION ALL
  SELECT run_id,launch_profile_id,launch_profile_revision,assignment_id,intent_id,attempt
  FROM run_retry_receipts
)
SELECT run.id
FROM runs run
JOIN retry_source source ON source.run_id=run.id
JOIN run_jobs job ON job.run_id=run.id
JOIN tasks task ON task.id=run.task_id
JOIN scheduling_intents intent ON intent.id=source.intent_id
JOIN task_assignments assignment ON assignment.id=run.assignment_id
JOIN launch_profiles profile ON profile.id=source.launch_profile_id
JOIN agents agent ON agent.id=run.agent_id
JOIN checkouts checkout ON checkout.id=run.checkout_id
WHERE run.workspace_id=? AND run.status='start_failed' AND run.step_cursor=0
  AND intent.status='run_requested' AND intent.run_id IS NOT NULL
  AND crewfold_timestamp_elapsed_seconds(?,run.updated_at)>=?
  AND job.status='complete' AND job.origin='supervisor'
  AND source.assignment_id=run.assignment_id
  AND source.attempt=(SELECT COUNT(*) FROM run_retry_receipts counted WHERE counted.intent_id=source.intent_id)
  AND (SELECT COUNT(*) FROM run_retry_receipts counted WHERE counted.intent_id=source.intent_id)<?
  AND NOT EXISTS (SELECT 1 FROM run_retry_receipts successor WHERE successor.prior_run_id=run.id)
  AND task.status='assigned'
  AND assignment.task_id=run.task_id AND assignment.agent_id=run.agent_id AND assignment.status='active'
  AND crewfold_timestamp_key(assignment.lease_expires_at)>crewfold_timestamp_key(?)
  AND profile.status='active' AND profile.revision=source.launch_profile_revision
  AND profile.manager_grant_id IS NULL AND profile.agent_id=run.agent_id
  AND profile.runtime=run.runtime AND profile.provider=run.provider
  AND (profile.checkout_id IS NULL OR profile.checkout_id=run.checkout_id)
  AND agent.enabled=1 AND agent.revision=profile.agent_revision
  AND agent.runtime=profile.runtime AND agent.provider=profile.provider
  AND checkout.project_id=run.project_id AND checkout.availability='available' AND checkout.write_mode<>'read_only'
  AND (checkout.write_mode='shared' OR NOT EXISTS (
    SELECT 1 FROM runs occupied WHERE occupied.checkout_id=checkout.id AND occupied.id<>run.id
      AND occupied.status IN ('requested','starting','active','blocked','stopping','lost')
  ))
  AND NOT EXISTS (
    SELECT 1 FROM task_claim_requirements requirement WHERE requirement.task_id=run.task_id
      AND NOT EXISTS (
        SELECT 1 FROM work_claims claim WHERE claim.task_id=requirement.task_id AND claim.status='active'
          AND crewfold_timestamp_key(claim.lease_expires_at)>crewfold_timestamp_key(?)
          AND claim.kind=requirement.kind AND claim.target=requirement.target
          AND claim.mode=requirement.mode AND claim.conflict_policy=requirement.conflict_policy
      )
  )
  AND (SELECT COUNT(*) FROM runs reserved WHERE reserved.workspace_id=run.workspace_id
       AND reserved.status IN ('requested','starting','active','blocked','stopping','lost'))<?
  AND (SELECT COUNT(*) FROM runs starting WHERE starting.workspace_id=run.workspace_id
       AND starting.status IN ('requested','starting'))<?
  AND (SELECT COUNT(*) FROM runs reserved WHERE reserved.project_id=run.project_id
       AND reserved.status IN ('requested','starting','active','blocked','stopping','lost'))<COALESCE((
         SELECT project_limit.max_concurrency FROM supervisor_policy_project_limits project_limit
         WHERE project_limit.workspace_id=? AND project_limit.policy_revision=? AND project_limit.project_id=run.project_id
       ),?)
  AND (SELECT COUNT(*) FROM runs reserved WHERE reserved.workspace_id=run.workspace_id AND reserved.provider=run.provider
       AND reserved.status IN ('requested','starting','active','blocked','stopping','lost'))<COALESCE((
         SELECT provider_limit.max_concurrency FROM supervisor_policy_provider_limits provider_limit
         WHERE provider_limit.workspace_id=? AND provider_limit.policy_revision=? AND provider_limit.provider=run.provider
       ),?)
  AND (SELECT COUNT(*) FROM runs reserved WHERE reserved.agent_id=run.agent_id
       AND reserved.status IN ('requested','starting','active','blocked','stopping','lost'))<agent.max_concurrency
ORDER BY run.updated_at,run.id LIMIT ?`,
		policy.WorkspaceID, now, policy.RetryCooldownSeconds, policy.AutoRetryLimit, now, now,
		policy.Limits.MaxActiveRuns, policy.Limits.MaxStartingRuns,
		policy.WorkspaceID, policy.Revision, policy.Limits.DefaultProjectConcurrency,
		policy.WorkspaceID, policy.Revision, policy.Limits.DefaultProviderConcurrency,
		maximumSupervisorQueueCandidates)
	if err != nil {
		return storageFailure("scan definite start failures", err)
	}
	runIDs := make([]string, 0)
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return storageFailure("scan definite start failure", err)
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Close(); err != nil {
		return storageFailure("close definite start failures", err)
	}
	nowTime, err := time.Parse(time.RFC3339Nano, now)
	if err != nil {
		return storageFailure("parse supervisor retry time", err)
	}
	for _, runID := range runIDs {
		if len(*actions) >= limit {
			break
		}
		run, err := queryRun(ctx, tx, policy.WorkspaceID, runID)
		if err != nil {
			return err
		}
		authority, eligible, err := s.supervisorRetryAuthorityForRun(ctx, tx, policy, run, now)
		if err != nil {
			return err
		}
		if !eligible {
			continue
		}
		failedAt, err := time.Parse(time.RFC3339Nano, run.UpdatedAt)
		if err != nil || nowTime.Before(failedAt.Add(time.Duration(policy.RetryCooldownSeconds)*time.Second)) {
			continue
		}
		capacity, err := supervisorRetryCapacityAvailable(ctx, tx, policy, run, authority.profile)
		if err != nil {
			return err
		}
		if !capacity {
			continue
		}
		action, newRunID, err := s.createSupervisorRetryRun(ctx, tx, policy, cursor, run, authority, correlationID, now)
		if err != nil {
			return err
		}
		*actions = append(*actions, action)
		*scheduled = append(*scheduled, newRunID)
	}
	return nil
}

func (s *Store) createSupervisorRetryRun(ctx context.Context, tx *sql.Tx, policy domain.SupervisorPolicy, cursor int64, prior domain.Run, authority supervisorRetryAuthority, correlationID, now string) (domain.SupervisorAction, string, error) {
	task, err := queryTask(ctx, tx, policy.WorkspaceID, prior.TaskID)
	if err != nil {
		return domain.SupervisorAction{}, "", err
	}
	agent, err := queryAgent(ctx, tx, policy.WorkspaceID, prior.AgentID)
	if err != nil {
		return domain.SupervisorAction{}, "", err
	}
	checkout, err := queryCheckoutByID(ctx, tx, prior.CheckoutID)
	if err != nil {
		return domain.SupervisorAction{}, "", err
	}
	packet, _, err := s.buildContextPacketInTransaction(ctx, tx, policy.WorkspaceID, task, agent, checkout, correlationID+"-context", now)
	if err != nil {
		return domain.SupervisorAction{}, "", err
	}
	newRunID, err := randomID("run_")
	if err != nil {
		return domain.SupervisorAction{}, "", storageFailure("generate bounded retry run id", err)
	}
	retryNumber := authority.retryCount + 1
	reasons := []string{
		fmt.Sprintf("prior run %s had a definite start failure", prior.ID),
		fmt.Sprintf("policy %d permits exact-profile retry %d of %d", policy.Revision, retryNumber, policy.AutoRetryLimit),
		fmt.Sprintf("profile %s fixes the same agent/runtime/provider/checkout", authority.profile.ID),
	}
	reasonsJSON, _ := json.Marshal(reasons)
	scenarioJSON, err := json.Marshal(authority.profile.Scenario)
	if err != nil {
		return domain.SupervisorAction{}, "", storageFailure("encode bounded retry scenario", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id,workspace_id,project_id,task_id,agent_id,checkout_id,runtime,provider,scenario_name,scenario_json,placement_reasons_json,status,step_cursor,revision,created_at,updated_at,created_by,updated_by,assignment_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,'requested',0,1,?,?,?,?,?)`,
		newRunID, prior.WorkspaceID, prior.ProjectID, prior.TaskID, prior.AgentID, prior.CheckoutID, prior.Runtime, prior.Provider,
		authority.profile.Scenario.Name, string(scenarioJSON), string(reasonsJSON), now, now, "subsystem:supervisor", "subsystem:supervisor", prior.AssignmentID); err != nil {
		return domain.SupervisorAction{}, "", storageFailure("insert bounded retry run", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_context_bindings(run_id,context_packet_id,bound_at) VALUES (?,?,?)`, newRunID, packet.ID, now); err != nil {
		return domain.SupervisorAction{}, "", storageFailure("bind bounded retry context", err)
	}
	if err := dbgen.New(tx).InsertRunContextDeltaState(ctx, dbgen.InsertRunContextDeltaStateParams{RunID: newRunID, ContextPacketID: packet.ID, ScanEventSequence: packet.AsOfEventSequence, CreatedAt: now, UpdatedAt: now}); err != nil {
		return domain.SupervisorAction{}, "", storageFailure("initialize bounded retry context delta", err)
	}
	nowTime, err := time.Parse(time.RFC3339Nano, now)
	if err != nil {
		return domain.SupervisorAction{}, "", storageFailure("parse bounded retry timestamp", err)
	}
	capabilityExpiry := nowTime.Add(time.Duration(authority.profile.CapabilityTTLSeconds) * time.Second).Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_capabilities(run_id,expires_at,created_at) VALUES (?,?,?)`, newRunID, capabilityExpiry, now); err != nil {
		return domain.SupervisorAction{}, "", storageFailure("create bounded retry capability", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_jobs(run_id,status,available_at,attempts,created_at,updated_at,origin) VALUES (?,'pending',?,0,?,?,'supervisor')`, newRunID, now, now, now); err != nil {
		return domain.SupervisorAction{}, "", storageFailure("enqueue bounded retry run", err)
	}
	if err := s.runMutationHook(MutationAfterRetryRun); err != nil {
		return domain.SupervisorAction{}, "", err
	}
	action, err := newSupervisorAction(policy, cursor, domain.SupervisorConditionFailed, domain.SupervisorResponseRetryTask, domain.SupervisorActionApplied,
		task, &prior, nil,
		[]string{"runtime start failed with a definite terminal result", "policy permits a bounded fresh-run same-profile retry after cooldown"},
		map[string]any{"launch_profile_id": authority.profile.ID, "launch_profile_revision": authority.profile.Revision,
			"retry_number": retryNumber, "retry_limit": policy.AutoRetryLimit, "retry_cooldown_seconds": policy.RetryCooldownSeconds,
			"assignment_id": prior.AssignmentID, "prior_run_id": prior.ID, "prior_failure_code": prior.FailureCode}, now)
	if err != nil {
		return domain.SupervisorAction{}, "", storageFailure("build bounded retry action", err)
	}
	action.PriorRunID, action.RunID, action.AppliedAt = prior.ID, newRunID, now
	action.ConditionKey = supervisorConditionKeyForAction(action)
	_, action.ContentSHA256, err = canonicalContent(supervisorActionContent{ConditionKey: action.ConditionKey, Condition: action.Condition, Response: action.Response,
		EntityRevision: action.EntityRevision, PolicyRevision: action.PolicyRevision, AsOfEventSequence: action.AsOfEventSequence,
		Reasons: action.Reasons, Constraints: action.ConstraintSnapshot})
	if err != nil {
		return domain.SupervisorAction{}, "", storageFailure("seal bounded retry action", err)
	}
	if err := insertSupervisorAction(ctx, tx, action); err != nil {
		return domain.SupervisorAction{}, "", err
	}
	actionSequence, err := appendEventForActor(ctx, tx, policy.WorkspaceID, "supervisor_action", action.ID, 1, supervisorActionAppliedEvent, correlationID, now, "subsystem:supervisor", "subsystem", map[string]any{
		"condition": action.Condition, "response": action.Response, "run_id": newRunID, "prior_run_id": prior.ID,
		"launch_profile_id": authority.profile.ID, "retry_number": retryNumber,
	})
	if err != nil {
		return domain.SupervisorAction{}, "", err
	}
	if err := s.sealSupervisorAction(ctx, tx, action, actionSequence); err != nil {
		return domain.SupervisorAction{}, "", err
	}
	message := fmt.Sprintf("bounded fresh-run same-profile retry %d of %d after definite start failure", retryNumber, policy.AutoRetryLimit)
	if err := appendRunTimeline(ctx, tx, newRunID, runRequestedEvent, message, nil, now); err != nil {
		return domain.SupervisorAction{}, "", err
	}
	if _, err := appendEventForActor(ctx, tx, policy.WorkspaceID, "run", newRunID, 1, runRequestedEvent, correlationID, now, "subsystem:supervisor", "subsystem", map[string]any{
		"supervisor_action_id": action.ID, "prior_run_id": prior.ID, "launch_profile_id": authority.profile.ID, "retry_number": retryNumber,
	}); err != nil {
		return domain.SupervisorAction{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_retry_receipts(run_id,workspace_id,prior_run_id,intent_id,action_id,launch_profile_id,launch_profile_revision,assignment_id,policy_revision,attempt,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		newRunID, policy.WorkspaceID, prior.ID, authority.intentID, action.ID, authority.profile.ID, authority.profile.Revision,
		prior.AssignmentID, policy.Revision, retryNumber, now); err != nil {
		return domain.SupervisorAction{}, "", storageFailure("seal bounded retry receipt", err)
	}
	if err := s.runMutationHook(MutationAfterRetryReceipt); err != nil {
		return domain.SupervisorAction{}, "", err
	}
	return action, newRunID, nil
}

func (s *Store) supervisorRetryAuthorityForRun(ctx context.Context, tx *sql.Tx, policy domain.SupervisorPolicy, run domain.Run, now string) (supervisorRetryAuthority, bool, error) {
	if run.Status != domain.RunStartFailed || run.StepCursor != 0 || policy.AutoRetryLimit < 1 {
		return supervisorRetryAuthority{}, false, nil
	}
	var profileID, intentID string
	var profileRevision int64
	var sourceAttempt int
	var assignmentID, jobStatus, jobOrigin, intentStatus string
	if err := tx.QueryRowContext(ctx, `SELECT source.launch_profile_id,source.launch_profile_revision,source.assignment_id,source.intent_id,source.attempt,job.status,job.origin,intent.status
FROM (
  SELECT launch_profile_id,launch_profile_revision,assignment_id,intent_id,0 AS attempt FROM run_scheduling_receipts WHERE run_id=?
  UNION ALL
  SELECT launch_profile_id,launch_profile_revision,assignment_id,intent_id,attempt FROM run_retry_receipts WHERE run_id=?
) source JOIN run_jobs job ON job.run_id=?
JOIN scheduling_intents intent ON intent.id=source.intent_id`, run.ID, run.ID, run.ID).Scan(&profileID, &profileRevision, &assignmentID, &intentID, &sourceAttempt, &jobStatus, &jobOrigin, &intentStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return supervisorRetryAuthority{}, false, nil
		}
		return supervisorRetryAuthority{}, false, storageFailure("query retry scheduling receipt", err)
	}
	if assignmentID != run.AssignmentID || jobStatus != "complete" || jobOrigin != "supervisor" || intentStatus != domain.SchedulingIntentRunRequested {
		return supervisorRetryAuthority{}, false, nil
	}
	profile, err := queryLaunchProfile(ctx, tx, policy.WorkspaceID, profileID)
	if err != nil {
		if ErrorCode(err) == CodeLaunchProfileNotFound {
			return supervisorRetryAuthority{}, false, nil
		}
		return supervisorRetryAuthority{}, false, err
	}
	if profile.Status != domain.LaunchProfileActive || profile.Revision != profileRevision || profile.AgentID != run.AgentID ||
		profile.Runtime != run.Runtime || profile.Provider != run.Provider || profile.ManagerGrantID != "" ||
		profile.CheckoutID != "" && profile.CheckoutID != run.CheckoutID {
		return supervisorRetryAuthority{}, false, nil
	}
	agent, err := queryAgent(ctx, tx, policy.WorkspaceID, run.AgentID)
	if err != nil {
		return supervisorRetryAuthority{}, false, err
	}
	if !agent.Enabled || agent.Revision != profile.AgentRevision || agent.Runtime != profile.Runtime || agent.Provider != profile.Provider {
		return supervisorRetryAuthority{}, false, nil
	}
	task, err := queryTask(ctx, tx, policy.WorkspaceID, run.TaskID)
	if err != nil {
		return supervisorRetryAuthority{}, false, err
	}
	if task.Status != domain.TaskAssigned || task.AssignmentID != run.AssignmentID || task.AssignedAgentID != run.AgentID {
		return supervisorRetryAuthority{}, false, nil
	}
	var assignmentOK, missingClaims, retryCount int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_assignments WHERE id=? AND task_id=? AND agent_id=? AND status='active' AND crewfold_timestamp_key(lease_expires_at)>crewfold_timestamp_key(?))`, run.AssignmentID, run.TaskID, run.AgentID, now).Scan(&assignmentOK); err != nil {
		return supervisorRetryAuthority{}, false, storageFailure("validate retry assignment", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_claim_requirements requirement WHERE requirement.task_id=? AND NOT EXISTS (
SELECT 1 FROM work_claims claim WHERE claim.task_id=requirement.task_id AND claim.status='active'
AND crewfold_timestamp_key(claim.lease_expires_at)>crewfold_timestamp_key(?) AND claim.kind=requirement.kind
AND claim.target=requirement.target AND claim.mode=requirement.mode AND claim.conflict_policy=requirement.conflict_policy)`, run.TaskID, now).Scan(&missingClaims); err != nil {
		return supervisorRetryAuthority{}, false, storageFailure("validate retry claims", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_retry_receipts WHERE intent_id=?`, intentID).Scan(&retryCount); err != nil {
		return supervisorRetryAuthority{}, false, storageFailure("count bounded retries", err)
	}
	if sourceAttempt != retryCount {
		return supervisorRetryAuthority{}, false, nil
	}
	return supervisorRetryAuthority{profile: profile, intentID: intentID, retryCount: retryCount}, assignmentOK == 1 && missingClaims == 0 && retryCount < policy.AutoRetryLimit, nil
}

func supervisorRetryCapacityAvailable(ctx context.Context, tx *sql.Tx, policy domain.SupervisorPolicy, run domain.Run, profile domain.LaunchProfile) (bool, error) {
	reserved := "('requested','starting','active','blocked','stopping','lost')"
	counts := make([]int, 5)
	queries := []struct {
		statement string
		arguments []any
	}{
		{`SELECT COUNT(*) FROM runs WHERE workspace_id=? AND status IN ` + reserved, []any{run.WorkspaceID}},
		{`SELECT COUNT(*) FROM runs WHERE workspace_id=? AND status IN ('requested','starting')`, []any{run.WorkspaceID}},
		{`SELECT COUNT(*) FROM runs WHERE project_id=? AND status IN ` + reserved, []any{run.ProjectID}},
		{`SELECT COUNT(*) FROM runs WHERE workspace_id=? AND provider=? AND status IN ` + reserved, []any{run.WorkspaceID, run.Provider}},
		{`SELECT COUNT(*) FROM runs WHERE agent_id=? AND status IN ` + reserved, []any{run.AgentID}},
	}
	for index, query := range queries {
		if err := tx.QueryRowContext(ctx, query.statement, query.arguments...).Scan(&counts[index]); err != nil {
			return false, storageFailure("count bounded retry capacity", err)
		}
	}
	projectLimit := policy.Limits.DefaultProjectConcurrency
	if value, exists := policy.Limits.ProjectConcurrency[run.ProjectID]; exists {
		projectLimit = value
	}
	providerLimit := policy.Limits.DefaultProviderConcurrency
	if value, exists := policy.Limits.ProviderConcurrency[profile.Provider]; exists {
		providerLimit = value
	}
	agent, err := queryAgent(ctx, tx, run.WorkspaceID, run.AgentID)
	if err != nil {
		return false, err
	}
	var checkoutWriteMode string
	var checkoutOccupied int
	if err := tx.QueryRowContext(ctx, `SELECT write_mode FROM checkouts WHERE id=? AND project_id=? AND availability='available' AND write_mode<>'read_only'`, run.CheckoutID, run.ProjectID).Scan(&checkoutWriteMode); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, storageFailure("validate bounded retry checkout", err)
	}
	if checkoutWriteMode != domain.WriteModeShared {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE checkout_id=? AND id<>? AND status IN ('requested','starting','active','blocked','stopping','lost')`, run.CheckoutID, run.ID).Scan(&checkoutOccupied); err != nil {
			return false, storageFailure("count bounded retry checkout capacity", err)
		}
	}
	return checkoutOccupied == 0 && counts[0] < policy.Limits.MaxActiveRuns && counts[1] < policy.Limits.MaxStartingRuns &&
		counts[2] < projectLimit && counts[3] < providerLimit && counts[4] < agent.MaxConcurrency, nil
}

func deferSchedulingIntent(ctx context.Context, tx *sql.Tx, intent *domain.SchedulingIntent, reason string, cursor int64, now string) (bool, error) {
	if len(reason) > 1024 {
		reason = reason[:1024]
	}
	nowTime, err := time.Parse(time.RFC3339Nano, now)
	if err != nil {
		return false, storageFailure("parse scheduling deferral time", err)
	}
	nextAttemptAt := nowTime.Add(supervisorSchedulingRetryDelay).UTC().Format(time.RFC3339Nano)
	recordAction := intent.Status != domain.SchedulingIntentDeferred || intent.Reason != reason
	intent.Status, intent.Reason, intent.Revision, intent.UpdatedAt, intent.UpdatedBy = domain.SchedulingIntentDeferred, reason, intent.Revision+1, now, "subsystem:supervisor"
	intent.NextAttemptAt = nextAttemptAt
	_, err = tx.ExecContext(ctx, `UPDATE scheduling_intents SET status='deferred',reason=?,revision=?,updated_at=?,next_attempt_at=?,last_evaluated_event_sequence=?,updated_by='subsystem:supervisor' WHERE id=?`, reason, intent.Revision, now, nextAttemptAt, cursor, intent.ID)
	if err != nil {
		return false, storageFailure("defer scheduling intent", err)
	}
	return recordAction, nil
}

func (s *Store) recordDeferredSchedulingAction(ctx context.Context, tx *sql.Tx, policy domain.SupervisorPolicy, cursor int64, task domain.Task, intent domain.SchedulingIntent, reason string, profile *domain.LaunchProfile, evidence map[string]any, correlationID, now string) (domain.SupervisorAction, error) {
	constraints := map[string]any{
		"policy_revision": policy.Revision, "intent_revision": intent.Revision,
		"max_active_runs": policy.Limits.MaxActiveRuns, "max_starting_runs": policy.Limits.MaxStartingRuns,
		"default_project_concurrency":  policy.Limits.DefaultProjectConcurrency,
		"default_provider_concurrency": policy.Limits.DefaultProviderConcurrency,
		"deferred_reason":              reason,
	}
	for key, value := range evidence {
		constraints[key] = value
	}
	if profile != nil {
		constraints["launch_profile_id"], constraints["launch_profile_revision"] = profile.ID, profile.Revision
		constraints["agent_id"], constraints["agent_revision"] = profile.AgentID, profile.AgentRevision
		constraints["runtime"], constraints["provider"] = profile.Runtime, profile.Provider
	}
	action, err := newSupervisorAction(policy, cursor, domain.SupervisorConditionDependencyReady, domain.SupervisorResponseSchedule,
		domain.SupervisorActionDeferred, task, nil, &intent,
		[]string{"accepted scheduling intent is dependency-ready", reason, "no assignment, claim, context packet, run, or job was created"}, constraints, now)
	if err != nil {
		return domain.SupervisorAction{}, storageFailure("build deferred scheduling action", err)
	}
	if err := insertSupervisorAction(ctx, tx, action); err != nil {
		return domain.SupervisorAction{}, err
	}
	actionSequence, err := appendEventForActor(ctx, tx, policy.WorkspaceID, "supervisor_action", action.ID, 1, supervisorActionRecordedEvent,
		correlationID, now, "subsystem:supervisor", "subsystem", map[string]any{
			"condition": action.Condition, "response": action.Response, "intent_id": intent.ID,
			"status": action.Status, "reason": reason,
		})
	if err != nil {
		return domain.SupervisorAction{}, err
	}
	if err := s.sealSupervisorAction(ctx, tx, action, actionSequence); err != nil {
		return domain.SupervisorAction{}, err
	}
	return action, nil
}

func (s *Store) scheduleIntentInTransaction(ctx context.Context, tx *sql.Tx, policy domain.SupervisorPolicy, intent domain.SchedulingIntent, task domain.Task, profile domain.LaunchProfile, action *domain.SupervisorAction, correlationID, now string) (string, error) {
	placement, err := s.preflightSchedulingPlacement(ctx, tx, policy, intent, task, profile, now)
	if action.ConstraintSnapshot == nil {
		action.ConstraintSnapshot = make(map[string]any)
	}
	for key, value := range placement.evidence {
		action.ConstraintSnapshot[key] = value
	}
	if err != nil {
		return "", err
	}
	agent, checkout := placement.agent, placement.checkout
	if err := s.materializeSchedulingClaims(ctx, tx, intent, task, checkout, profile, placement.claims, correlationID, now, action.ConstraintSnapshot); err != nil {
		return "", err
	}
	if err := s.runMutationHook(MutationAfterSchedulingAuthority); err != nil {
		return "", err
	}
	assignmentID, err := randomID("asg_")
	if err != nil {
		return "", storageFailure("generate supervisor assignment", err)
	}
	leaseExpiry := s.clock().UTC().Add(time.Duration(profile.AssignmentLeaseSeconds) * time.Second).Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_assignments(id,task_id,agent_id,status,lease_expires_at,revision,created_at,updated_at,created_by,updated_by) VALUES (?,?,?,'active',?,1,?,?,?,?)`, assignmentID, task.ID, agent.ID, leaseExpiry, now, now, "subsystem:supervisor", "subsystem:supervisor"); err != nil {
		return "", storageFailure("insert supervisor assignment", err)
	}
	task.Status, task.AssignmentID, task.AssignedAgentID, task.AssignmentLeaseExpiresAt = domain.TaskAssigned, assignmentID, agent.ID, leaseExpiry
	task.Revision++
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status='assigned',revision=?,updated_at=?,updated_by=? WHERE id=?`, task.Revision, now, "subsystem:supervisor", task.ID); err != nil {
		return "", storageFailure("assign supervisor task", err)
	}
	if _, err := appendEventForActor(ctx, tx, intent.WorkspaceID, "task", task.ID, task.Revision, taskAssigned, correlationID, now, "subsystem:supervisor", "subsystem", map[string]any{"assignment_id": assignmentID, "agent_id": agent.ID, "lease_expires_at": leaseExpiry, "intent_id": intent.ID}); err != nil {
		return "", err
	}
	packet, _, err := s.buildContextPacketInTransaction(ctx, tx, intent.WorkspaceID, task, agent, checkout, correlationID+"-context", now)
	if err != nil {
		return "", err
	}
	reasons := []string{"accepted scheduling intent is dependency-ready", fmt.Sprintf("policy %d allows automatic scheduling", policy.Revision), fmt.Sprintf("profile %s fixes agent/runtime/provider", profile.ID)}
	reasonsJSON, _ := json.Marshal(reasons)
	scenarioJSON, err := json.Marshal(profile.Scenario)
	if err != nil {
		return "", storageFailure("encode scheduled scenario", err)
	}
	runID, err := randomID("run_")
	if err != nil {
		return "", storageFailure("generate scheduled run", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id,workspace_id,project_id,task_id,agent_id,checkout_id,runtime,provider,scenario_name,scenario_json,placement_reasons_json,status,step_cursor,revision,created_at,updated_at,created_by,updated_by,assignment_id) VALUES (?,?,?,?,?,?,?,?,?,?,?,'requested',0,1,?,?,?,?,?)`,
		runID, intent.WorkspaceID, task.ProjectID, task.ID, agent.ID, checkout.ID, profile.Runtime, profile.Provider, profile.Scenario.Name, string(scenarioJSON), string(reasonsJSON), now, now, "subsystem:supervisor", "subsystem:supervisor", assignmentID); err != nil {
		return "", storageFailure("insert scheduled run", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_context_bindings(run_id,context_packet_id,bound_at) VALUES (?,?,?)`, runID, packet.ID, now); err != nil {
		return "", storageFailure("bind scheduled context", err)
	}
	if err := dbgen.New(tx).InsertRunContextDeltaState(ctx, dbgen.InsertRunContextDeltaStateParams{RunID: runID, ContextPacketID: packet.ID, ScanEventSequence: packet.AsOfEventSequence, CreatedAt: now, UpdatedAt: now}); err != nil {
		return "", storageFailure("initialize scheduled context delta", err)
	}
	capabilityExpiry := s.clock().UTC().Add(time.Duration(profile.CapabilityTTLSeconds) * time.Second).Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_capabilities(run_id,expires_at,created_at) VALUES (?,?,?)`, runID, capabilityExpiry, now); err != nil {
		return "", storageFailure("create scheduled capability", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_jobs(run_id,status,available_at,attempts,created_at,updated_at,origin) VALUES (?,'pending',?,0,?,?,'supervisor')`, runID, now, now, now); err != nil {
		return "", storageFailure("enqueue scheduled run", err)
	}
	if err := s.runMutationHook(MutationAfterSchedulingRun); err != nil {
		return "", err
	}
	action.RunID = runID
	action.ConditionKey = supervisorConditionKeyForAction(*action)
	_, action.ContentSHA256, err = canonicalContent(supervisorActionContent{ConditionKey: action.ConditionKey, Condition: action.Condition, Response: action.Response,
		EntityRevision: action.EntityRevision, PolicyRevision: action.PolicyRevision, AsOfEventSequence: action.AsOfEventSequence,
		Reasons: action.Reasons, Constraints: action.ConstraintSnapshot})
	if err != nil {
		return "", storageFailure("seal scheduled supervisor action", err)
	}
	if err := insertSupervisorAction(ctx, tx, *action); err != nil {
		return "", err
	}
	actionSequence, err := appendEventForActor(ctx, tx, intent.WorkspaceID, "supervisor_action", action.ID, 1, supervisorActionAppliedEvent, correlationID, now, "subsystem:supervisor", "subsystem", map[string]any{"condition": action.Condition, "response": action.Response, "intent_id": intent.ID, "run_id": runID})
	if err != nil {
		return "", err
	}
	if err := s.sealSupervisorAction(ctx, tx, *action, actionSequence); err != nil {
		return "", err
	}
	if err := s.runMutationHook(MutationAfterSchedulingAction); err != nil {
		return "", err
	}
	if err := appendRunTimeline(ctx, tx, runID, runRequestedEvent, "supervisor scheduled accepted intent", nil, now); err != nil {
		return "", err
	}
	if _, err := appendEventForActor(ctx, tx, intent.WorkspaceID, "run", runID, 1, runRequestedEvent, correlationID, now, "subsystem:supervisor", "subsystem", map[string]any{"intent_id": intent.ID, "action_id": action.ID, "launch_profile_id": profile.ID}); err != nil {
		return "", err
	}
	intent.Status, intent.AssignmentID, intent.RunID, intent.SupervisorActionID = domain.SchedulingIntentRunRequested, assignmentID, runID, action.ID
	intent.Attempts++
	intent.Revision++
	if _, err := tx.ExecContext(ctx, `UPDATE scheduling_intents SET status='run_requested',reason=NULL,assignment_id=?,run_id=?,supervisor_action_id=?,attempts=?,revision=?,updated_at=?,next_attempt_at=NULL,last_evaluated_event_sequence=?,updated_by='subsystem:supervisor' WHERE id=?`, assignmentID, runID, action.ID, intent.Attempts, intent.Revision, now, action.AsOfEventSequence, intent.ID); err != nil {
		return "", storageFailure("advance scheduling intent", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_scheduling_receipts(run_id,workspace_id,intent_id,action_id,launch_profile_id,launch_profile_revision,assignment_id,task_revision,policy_revision,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, runID, intent.WorkspaceID, intent.ID, action.ID, profile.ID, profile.Revision, assignmentID, task.Revision, policy.Revision, now); err != nil {
		return "", storageFailure("seal scheduling receipt", err)
	}
	if err := s.runMutationHook(MutationAfterSchedulingReceipt); err != nil {
		return "", err
	}
	return runID, nil
}

func (s *Store) materializeNonAutomaticSupervisorConditions(ctx context.Context, tx *sql.Tx, policy domain.SupervisorPolicy, cursor int64, limit int, actions *[]domain.SupervisorAction, correlationID, now string) error {
	if limit <= 0 {
		return nil
	}
	type candidate struct {
		runID, condition, response, reason string
	}
	rows, err := tx.QueryContext(ctx, `WITH retry_source AS (
  SELECT run_id,launch_profile_id,launch_profile_revision,assignment_id,intent_id,0 AS attempt
  FROM run_scheduling_receipts
  UNION ALL
  SELECT run_id,launch_profile_id,launch_profile_revision,assignment_id,intent_id,attempt
  FROM run_retry_receipts
), retry_authorized AS (
  SELECT run.id
  FROM runs run
  JOIN retry_source source ON source.run_id=run.id
  JOIN run_jobs job ON job.run_id=run.id
  JOIN tasks task ON task.id=run.task_id
  JOIN scheduling_intents intent ON intent.id=source.intent_id
  JOIN task_assignments assignment ON assignment.id=run.assignment_id
  JOIN launch_profiles profile ON profile.id=source.launch_profile_id
  JOIN agents agent ON agent.id=run.agent_id
  WHERE run.status='start_failed' AND run.step_cursor=0
    AND intent.status='run_requested'
    AND job.status='complete' AND job.origin='supervisor'
    AND source.assignment_id=run.assignment_id
    AND source.attempt=(SELECT COUNT(*) FROM run_retry_receipts counted WHERE counted.intent_id=source.intent_id)
    AND (SELECT COUNT(*) FROM run_retry_receipts counted WHERE counted.intent_id=source.intent_id)<?
    AND NOT EXISTS (SELECT 1 FROM run_retry_receipts successor WHERE successor.prior_run_id=run.id)
    AND task.status='assigned'
    AND assignment.task_id=run.task_id AND assignment.agent_id=run.agent_id AND assignment.status='active'
    AND crewfold_timestamp_key(assignment.lease_expires_at)>crewfold_timestamp_key(?)
    AND profile.status='active' AND profile.revision=source.launch_profile_revision
    AND profile.manager_grant_id IS NULL AND profile.agent_id=run.agent_id
    AND profile.runtime=run.runtime AND profile.provider=run.provider
    AND (profile.checkout_id IS NULL OR profile.checkout_id=run.checkout_id)
    AND agent.enabled=1 AND agent.revision=profile.agent_revision
    AND agent.runtime=profile.runtime AND agent.provider=profile.provider
    AND NOT EXISTS (
      SELECT 1 FROM task_claim_requirements requirement WHERE requirement.task_id=run.task_id
        AND NOT EXISTS (
          SELECT 1 FROM work_claims claim WHERE claim.task_id=requirement.task_id AND claim.status='active'
            AND crewfold_timestamp_key(claim.lease_expires_at)>crewfold_timestamp_key(?)
            AND claim.kind=requirement.kind AND claim.target=requirement.target
            AND claim.mode=requirement.mode AND claim.conflict_policy=requirement.conflict_policy
        )
    )
)
SELECT run.id,
  CASE WHEN run.status='lost' THEN 'stale'
       WHEN (SELECT COUNT(*) FROM runs failed WHERE failed.task_id=run.task_id
             AND failed.status IN ('failed','start_failed'))>=2 THEN 'repeated_failure'
       ELSE 'failed' END AS condition
FROM runs run
WHERE run.workspace_id=? AND run.status IN ('lost','failed','start_failed')
  AND run.id=(SELECT latest.id FROM runs latest WHERE latest.task_id=run.task_id
              ORDER BY latest.created_at DESC,latest.id DESC LIMIT 1)
  AND NOT EXISTS (SELECT 1 FROM run_retry_receipts successor WHERE successor.prior_run_id=run.id)
  AND (run.status<>'start_failed' OR run.id NOT IN (SELECT id FROM retry_authorized))
  AND NOT EXISTS (
    SELECT 1 FROM supervisor_actions action
    JOIN supervisor_action_receipts receipt ON receipt.action_id=action.id
    WHERE action.workspace_id=run.workspace_id AND action.run_id=run.id
      AND action.condition=CASE WHEN run.status='lost' THEN 'stale'
        WHEN (SELECT COUNT(*) FROM runs failed WHERE failed.task_id=run.task_id
              AND failed.status IN ('failed','start_failed'))>=2 THEN 'repeated_failure'
        ELSE 'failed' END
      AND action.response='request_owner' AND action.entity_revision=run.revision
      AND receipt.condition_key=action.condition_key
  )
ORDER BY run.updated_at,run.id LIMIT ?`, policy.AutoRetryLimit, now, now, policy.WorkspaceID, limit)
	if err != nil {
		return storageFailure("scan nonautomatic run conditions", err)
	}
	values := make([]candidate, 0)
	for rows.Next() {
		var id, condition string
		if err := rows.Scan(&id, &condition); err != nil {
			rows.Close()
			return err
		}
		reason := "terminal or uncertain run state requires an owner decision"
		if condition == domain.SupervisorConditionRepeatedFailure {
			reason = "task has two or more definite failed starts or runs"
		}
		values = append(values, candidate{id, condition, domain.SupervisorResponseRequestOwner, reason})
	}
	if err := rows.Close(); err != nil {
		return storageFailure("close nonautomatic run conditions", err)
	}
	remaining := limit - len(values)
	if remaining < 0 {
		remaining = 0
	}
	overBudgetRows, err := tx.QueryContext(ctx, `
SELECT run.id FROM runs run JOIN tasks task ON task.id=run.task_id
WHERE run.workspace_id=? AND run.status IN ('active','blocked','stopping','lost')
  AND run.started_at IS NOT NULL AND task.budget_time_seconds>0
	AND crewfold_timestamp_elapsed_seconds(?,run.started_at) > task.budget_time_seconds
  AND NOT EXISTS (
    SELECT 1 FROM supervisor_actions action
    JOIN supervisor_action_receipts receipt ON receipt.action_id=action.id
    WHERE action.workspace_id=run.workspace_id AND action.run_id=run.id
      AND action.condition='over_budget' AND action.response='request_owner'
      AND action.entity_revision=run.revision AND receipt.condition_key=action.condition_key
  )
ORDER BY run.updated_at,run.id LIMIT ?`, policy.WorkspaceID, now, remaining)
	if err != nil {
		return storageFailure("scan wall-time budget conditions", err)
	}
	for overBudgetRows.Next() {
		var id string
		if err := overBudgetRows.Scan(&id); err != nil {
			overBudgetRows.Close()
			return err
		}
		values = append(values, candidate{id, domain.SupervisorConditionOverBudget, domain.SupervisorResponseRequestOwner, "trusted start time exceeds the task wall-time allocation"})
	}
	overBudgetRows.Close()
	created := 0
	for _, value := range values {
		if created >= limit {
			break
		}
		run, err := queryRun(ctx, tx, policy.WorkspaceID, value.runID)
		if err != nil {
			return err
		}
		var hasRetrySuccessor int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_retry_receipts WHERE prior_run_id=?)`, run.ID).Scan(&hasRetrySuccessor); err != nil {
			return storageFailure("check retry successor", err)
		}
		if hasRetrySuccessor == 1 {
			// The prior definite failure is already durably answered by its fresh
			// retry run. Only the latest run in the retry chain may materialize a
			// further condition or owner decision.
			continue
		}
		if run.Status == domain.RunStartFailed && policy.AutoRetryLimit > 0 {
			_, retryStillAuthorized, err := s.supervisorRetryAuthorityForRun(ctx, tx, policy, run, now)
			if err != nil {
				return err
			}
			if retryStillAuthorized {
				// Cooldown or current capacity may delay the automatic retry, but
				// neither condition widens it into an owner action.
				continue
			}
		}
		if run.Status == domain.RunStartFailed {
			if err := terminalizeSchedulingIntentForRun(ctx, tx, run, "definite start failure has no remaining authorized retry", correlationID, now); err != nil {
				return err
			}
		}
		key := supervisorConditionKey(value.condition, value.response, "", run.TaskID, run.ID, run.Revision)
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM supervisor_action_receipts WHERE condition_key=?`, key).Scan(&exists); err != nil || exists != 0 {
			if err != nil {
				return err
			}
			continue
		}
		task, err := queryTask(ctx, tx, policy.WorkspaceID, run.TaskID)
		if err != nil {
			return err
		}
		action, err := newSupervisorAction(policy, cursor, value.condition, value.response, domain.SupervisorActionAwaitingApproval, task, &run, nil,
			[]string{value.reason, "condition is outside automatic policy and has no effect before owner approval"},
			map[string]any{"run_status": run.Status, "failure_code": run.FailureCode, "task_time_budget_seconds": task.Budget.TimeSeconds, "run_started_at": run.StartedAt}, now)
		if err != nil {
			return err
		}
		approvalID, err := randomID("appr_")
		if err != nil {
			return storageFailure("generate supervisor approval", err)
		}
		action.ApprovalID = approvalID
		if err := insertSupervisorAction(ctx, tx, action); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO approval_requests(id,workspace_id,action_id,status,expected_action_revision,revision,created_at,updated_at,created_by,updated_by) VALUES (?,?,?,'pending',1,1,?,?,?,?)`, approvalID, policy.WorkspaceID, action.ID, now, now, "subsystem:supervisor", "subsystem:supervisor"); err != nil {
			return storageFailure("insert supervisor approval", err)
		}
		actionSequence, err := appendEventForActor(ctx, tx, policy.WorkspaceID, "supervisor_action", action.ID, 1, supervisorActionRecordedEvent, correlationID, now, "subsystem:supervisor", "subsystem", map[string]any{"condition": action.Condition, "response": action.Response, "approval_id": approvalID})
		if err != nil {
			return err
		}
		if err := s.sealSupervisorAction(ctx, tx, action, actionSequence); err != nil {
			return err
		}
		if _, err := appendEventForActor(ctx, tx, policy.WorkspaceID, "approval_request", approvalID, 1, approvalRequestedEvent, correlationID, now, "subsystem:supervisor", "subsystem", map[string]any{"action_id": action.ID}); err != nil {
			return err
		}
		*actions = append(*actions, action)
		created++
	}
	return nil
}

const supervisorActionSelect = `SELECT id,workspace_id,COALESCE(project_id,''),COALESCE(objective_id,''),COALESCE(task_id,''),COALESCE(run_id,''),COALESCE(prior_run_id,''),COALESCE(source_proposal_id,''),COALESCE(source_action_id,''),COALESCE(agent_id,''),COALESCE(intent_id,''),condition,condition_key,response,status,COALESCE(decision,''),entity_revision,policy_revision,as_of_event_sequence,reasons_json,constraint_snapshot_json,content_sha256,COALESCE(approval_id,''),revision,created_at,updated_at,COALESCE(applied_at,''),created_by,updated_by FROM supervisor_actions`

func scanSupervisorAction(row rowScanner) (domain.SupervisorAction, error) {
	var value domain.SupervisorAction
	var reasonsJSON, constraintsJSON string
	if err := row.Scan(&value.ID, &value.WorkspaceID, &value.ProjectID, &value.ObjectiveID, &value.TaskID, &value.RunID, &value.PriorRunID, &value.SourceProposalID, &value.SourceActionID,
		&value.AgentID, &value.IntentID, &value.Condition, &value.ConditionKey, &value.Response, &value.Status, &value.Decision,
		&value.EntityRevision, &value.PolicyRevision, &value.AsOfEventSequence, &reasonsJSON, &constraintsJSON,
		&value.ContentSHA256, &value.ApprovalID, &value.Revision, &value.CreatedAt, &value.UpdatedAt, &value.AppliedAt,
		&value.CreatedBy, &value.UpdatedBy); err != nil {
		return domain.SupervisorAction{}, err
	}
	if err := json.Unmarshal([]byte(reasonsJSON), &value.Reasons); err != nil {
		return domain.SupervisorAction{}, storageFailure("decode supervisor reasons", err)
	}
	if err := json.Unmarshal([]byte(constraintsJSON), &value.ConstraintSnapshot); err != nil {
		return domain.SupervisorAction{}, storageFailure("decode supervisor constraints", err)
	}
	return value, nil
}

func querySupervisorAction(ctx context.Context, database queryRower, workspaceID, actionID string) (domain.SupervisorAction, error) {
	value, err := scanSupervisorAction(database.QueryRowContext(ctx, supervisorActionSelect+` WHERE workspace_id=? AND id=? AND EXISTS (
SELECT 1 FROM supervisor_action_receipts receipt WHERE receipt.action_id=supervisor_actions.id AND receipt.condition_key=supervisor_actions.condition_key)`, workspaceID, actionID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SupervisorAction{}, &Error{Code: CodeSupervisorActionNotFound, Message: fmt.Sprintf("supervisor action %q was not found", actionID)}
	}
	if err != nil {
		return domain.SupervisorAction{}, storageFailure("query supervisor action", err)
	}
	return value, nil
}

func (s *Store) SupervisorAction(ctx context.Context, workspaceIdentifier, actionID string) (domain.SupervisorAction, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.SupervisorAction{}, err
	}
	return querySupervisorAction(ctx, s.db, workspace.ID, strings.TrimSpace(actionID))
}

func (s *Store) SupervisorActions(ctx context.Context, query ListSupervisorActionsQuery) ([]domain.SupervisorAction, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return nil, err
	}
	statement := supervisorActionSelect + ` WHERE workspace_id=? AND EXISTS (
SELECT 1 FROM supervisor_action_receipts receipt WHERE receipt.action_id=supervisor_actions.id AND receipt.condition_key=supervisor_actions.condition_key)`
	arguments := []any{workspace.ID}
	if value := strings.TrimSpace(query.ProjectIdentifier); value != "" {
		project, err := queryProject(ctx, s.db, workspace.ID, value)
		if err != nil {
			return nil, err
		}
		statement += ` AND project_id=?`
		arguments = append(arguments, project.ID)
	}
	for _, filter := range []struct{ column, value string }{{"task_id", query.TaskID}, {"run_id", query.RunID}, {"status", query.Status}, {"condition", query.Condition}} {
		if value := strings.TrimSpace(filter.value); value != "" {
			statement += ` AND ` + filter.column + `=?`
			arguments = append(arguments, value)
		}
	}
	statement += ` ORDER BY created_at,id LIMIT ?`
	arguments = append(arguments, boundedManagementLimit(query.Limit))
	rows, err := s.db.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, storageFailure("list supervisor actions", err)
	}
	defer rows.Close()
	result := make([]domain.SupervisorAction, 0)
	for rows.Next() {
		value, err := scanSupervisorAction(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) ExplainSupervisor(ctx context.Context, query ExplainSupervisorQuery) (domain.SupervisorExplanation, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return domain.SupervisorExplanation{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.SupervisorExplanation{}, storageFailure("begin supervisor explanation", err)
	}
	defer tx.Rollback()
	policy, err := querySupervisorPolicy(ctx, tx, workspace.ID)
	if err != nil {
		return domain.SupervisorExplanation{}, err
	}
	journal, err := inspectSupervisorJournal(ctx, tx, workspace.ID)
	if err != nil {
		return domain.SupervisorExplanation{}, err
	}
	if !journal.CaughtUp {
		return domain.SupervisorExplanation{}, &Error{
			Code:    CodeRetrievalDegraded,
			Message: fmt.Sprintf("supervisor explanation is classified through event %d but workspace facts extend through %d", journal.From, journal.Cutoff),
		}
	}
	cursor := journal.Cutoff
	now := s.nowText()
	statement := schedulingIntentSelect + ` AS intent WHERE intent.workspace_id=? AND intent.status IN ('pending','deferred')`
	arguments := []any{workspace.ID}
	if value := strings.TrimSpace(query.IntentID); value != "" {
		statement += ` AND intent.id=?`
		arguments = append(arguments, value)
	}
	if value := strings.TrimSpace(query.TaskID); value != "" {
		statement += ` AND intent.task_id=?`
		arguments = append(arguments, value)
	}
	statement += ` ORDER BY
  (SELECT task.priority FROM tasks task WHERE task.id=intent.task_id) DESC, ` +
		schedulingIntentEligibilityKeySQL + `,intent.task_id,intent.id LIMIT ?`
	arguments = append(arguments, boundedManagementLimit(query.Limit))
	rows, err := tx.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return domain.SupervisorExplanation{}, storageFailure("query supervisor explanation", err)
	}
	defer rows.Close()
	result := domain.SupervisorExplanation{WorkspaceID: workspace.ID, Policy: policy, AsOfEventSequence: cursor, Candidates: make([]domain.SupervisorCandidateExplanation, 0)}
	for rows.Next() {
		intent, err := scanSchedulingIntent(rows)
		if err != nil {
			return domain.SupervisorExplanation{}, err
		}
		task, err := queryTask(ctx, tx, workspace.ID, intent.TaskID)
		if err != nil {
			return domain.SupervisorExplanation{}, err
		}
		profile, err := queryLaunchProfile(ctx, tx, workspace.ID, intent.LaunchProfileID)
		if err != nil {
			if ErrorCode(err) != CodeLaunchProfileNotFound {
				return domain.SupervisorExplanation{}, err
			}
			profile = domain.LaunchProfile{ID: intent.LaunchProfileID, WorkspaceID: intent.WorkspaceID,
				ProjectID: intent.ProjectID, AgentID: intent.AgentID}
		}
		placement, placementErr := s.preflightSchedulingPlacement(ctx, tx, policy, intent, task, profile, now)
		eligible := placementErr == nil && policy.Enabled && policy.AutoSchedule
		reasons := make([]string, 0)
		if typed := (*supervisorDeferralError)(nil); errors.As(placementErr, &typed) {
			reasons = append(reasons, typed.cause.Message)
		} else if placementErr != nil {
			return domain.SupervisorExplanation{}, placementErr
		}
		if !policy.Enabled {
			eligible = false
			reasons = append(reasons, "supervisor policy is disabled")
		} else if !policy.AutoSchedule {
			eligible = false
			reasons = append(reasons, "automatic scheduling is disabled")
		}
		wakeRelevant := false
		if intent.NextAttemptAt != "" && timestampAfterInstant(intent.NextAttemptAt, mustParseManagementTime(now)) {
			var wake int
			if err := tx.QueryRowContext(ctx, `SELECT `+schedulingIntentRelevantWakeSQL+` FROM scheduling_intents intent WHERE intent.id=?`, cursor, intent.ID).Scan(&wake); err != nil {
				return domain.SupervisorExplanation{}, storageFailure("evaluate supervisor explanation wake", err)
			}
			wakeRelevant = wake == 1
		}
		if intent.NextAttemptAt != "" && timestampAfterInstant(intent.NextAttemptAt, mustParseManagementTime(now)) && !wakeRelevant {
			eligible = false
			reasons = append(reasons, "deferred retry time has not arrived")
			placement.evidence["next_attempt_at"] = intent.NextAttemptAt
		} else if wakeRelevant {
			placement.evidence["retry_backoff_bypassed_by_relevant_event"] = true
		}
		if intent.Status == domain.SchedulingIntentDeferred && intent.Reason != "" {
			reasons = append(reasons, "previous deferral: "+intent.Reason)
		}
		if len(reasons) == 0 {
			reasons = append(reasons, "complete placement preflight is eligible")
		}
		result.Candidates = append(result.Candidates, domain.SupervisorCandidateExplanation{IntentID: intent.ID, TaskID: intent.TaskID,
			Eligible: eligible, Reasons: reasons, Constraints: placement.evidence})
	}
	return result, rows.Err()
}

func mustParseManagementTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func scanSchedulingIntent(row rowScanner) (domain.SchedulingIntent, error) {
	var value domain.SchedulingIntent
	err := row.Scan(&value.ID, &value.WorkspaceID, &value.ProjectID, &value.ObjectiveID, &value.TaskID, &value.AgentID, &value.LaunchProfileID,
		&value.SourceProposalID, &value.SourceActionID, &value.SourceCheckRepairProposalID, &value.Status, &value.Reason, &value.AssignmentID, &value.RunID,
		&value.SupervisorActionID, &value.Attempts, &value.LastEvaluatedEventSequence, &value.Revision, &value.CreatedAt, &value.UpdatedAt, &value.NextAttemptAt,
		&value.CreatedBy, &value.UpdatedBy)
	return value, err
}

const approvalRequestSelect = `SELECT id,workspace_id,COALESCE((SELECT action.project_id FROM supervisor_actions action WHERE action.id=approval_requests.action_id),''),action_id,status,COALESCE(decision_note,''),COALESCE(decision_event_sequence,0),expected_action_revision,revision,COALESCE(expires_at,''),created_at,updated_at,COALESCE(decided_at,''),created_by,updated_by,COALESCE(decided_by,'') FROM approval_requests`

func scanApprovalRequest(row rowScanner) (domain.ApprovalRequest, error) {
	var value domain.ApprovalRequest
	err := row.Scan(&value.ID, &value.WorkspaceID, &value.ProjectID, &value.ActionID, &value.Status, &value.DecisionNote, &value.DecisionEventSequence,
		&value.ExpectedActionRevision, &value.Revision, &value.ExpiresAt, &value.CreatedAt, &value.UpdatedAt, &value.DecidedAt,
		&value.CreatedBy, &value.UpdatedBy, &value.DecidedBy)
	return value, err
}

func queryApprovalRequest(ctx context.Context, database queryRower, workspaceID, approvalID string) (domain.ApprovalRequest, error) {
	value, err := scanApprovalRequest(database.QueryRowContext(ctx, approvalRequestSelect+` WHERE workspace_id=? AND id=? AND EXISTS (
SELECT 1 FROM supervisor_actions action JOIN supervisor_action_receipts receipt ON receipt.action_id=action.id
WHERE action.id=approval_requests.action_id AND receipt.condition_key=action.condition_key)`, workspaceID, approvalID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ApprovalRequest{}, &Error{Code: CodeApprovalNotFound, Message: fmt.Sprintf("approval request %q was not found", approvalID)}
	}
	if err != nil {
		return domain.ApprovalRequest{}, storageFailure("query approval request", err)
	}
	return value, nil
}

func (s *Store) ApprovalRequest(ctx context.Context, workspaceIdentifier, approvalID string) (domain.ApprovalRequest, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	return queryApprovalRequest(ctx, s.db, workspace.ID, strings.TrimSpace(approvalID))
}

func (s *Store) ApprovalRequests(ctx context.Context, query ListApprovalRequestsQuery) (ApprovalPage, error) {
	limit, err := readPageLimit(query.Limit, MaximumReadPageLimit)
	if err != nil {
		return ApprovalPage{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ApprovalPage{}, storageFailure("begin approval page snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(query.WorkspaceIdentifier))
	if err != nil {
		return ApprovalPage{}, err
	}
	projectID, err := resolveOptionalProjectID(ctx, tx, workspace.ID, query.ProjectIdentifier)
	if err != nil {
		return ApprovalPage{}, err
	}
	status := strings.TrimSpace(query.Status)
	actionID := strings.TrimSpace(query.ActionID)
	fingerprint := readQueryFingerprint("approval.list", workspace.ID, projectID, status, actionID)
	cursor, err := decodeRecordCursor(query.Cursor, fingerprint)
	if err != nil {
		return ApprovalPage{}, err
	}
	queries := dbgen.New(tx)
	total, err := queries.CountOperatorApprovals(ctx, dbgen.CountOperatorApprovalsParams{WorkspaceID: workspace.ID, ProjectID: projectID, Status: status, ActionID: actionID})
	if err != nil {
		return ApprovalPage{}, storageFailure("count approval requests", err)
	}
	ids, err := queries.ListOperatorApprovalIDs(ctx, dbgen.ListOperatorApprovalIDsParams{
		WorkspaceID: workspace.ID, ProjectID: projectID, Status: status, ActionID: actionID, CursorKey: cursor.Key,
		CursorID: cursor.ID, ResultLimit: int64(limit + 1),
	})
	if err != nil {
		return ApprovalPage{}, storageFailure("list approval request ids", err)
	}
	hasMore := len(ids) > limit
	if hasMore {
		ids = ids[:limit]
	}
	values := make([]domain.ApprovalRequest, 0, len(ids))
	for _, id := range ids {
		value, err := queryApprovalRequest(ctx, tx, workspace.ID, id)
		if err != nil {
			return ApprovalPage{}, err
		}
		values = append(values, value)
	}
	next, err := nextRecordCursor(fingerprint, hasMore, values, func(value domain.ApprovalRequest) (string, string) { return value.CreatedAt, value.ID })
	if err != nil {
		return ApprovalPage{}, err
	}
	return ApprovalPage{Approvals: values, NextCursor: next, HasMore: hasMore, Total: total}, nil
}

func (s *Store) AllowApproval(ctx context.Context, command DecideApprovalCommand) (ApprovalMutationResult, error) {
	return s.decideApproval(ctx, command, true)
}

func (s *Store) DenyApproval(ctx context.Context, command DecideApprovalCommand) (ApprovalMutationResult, error) {
	return s.decideApproval(ctx, command, false)
}

func (s *Store) decideApproval(ctx context.Context, command DecideApprovalCommand, allow bool) (ApprovalMutationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ApprovalRequestID = strings.TrimSpace(command.ApprovalRequestID)
	command.DecisionNote = strings.TrimSpace(command.DecisionNote)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.ApprovalRequestID == "" || command.ExpectedRevision < 1 || !validDecisionNote(command.DecisionNote) {
		return ApprovalMutationResult{}, &Error{Code: CodeApprovalConflict, Message: "approval decision requires workspace, approval, expected revision, and a decision note from 1 to 1024 bytes"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeApprovalConflict); err != nil {
		return ApprovalMutationResult{}, err
	}
	operation := "approval.deny"
	if allow {
		operation = "approval.allow"
	}
	requestHash, err := hashManagementCommand(operation, command)
	if err != nil {
		return ApprovalMutationResult{}, storageFailure("hash approval decision", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApprovalMutationResult{}, storageFailure("begin approval decision", err)
	}
	defer tx.Rollback()
	var replay ApprovalMutationResult
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, operation, requestHash, &replay); err != nil {
		return ApprovalMutationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return ApprovalMutationResult{}, err
	}
	approval, err := queryApprovalRequest(ctx, tx, workspace.ID, command.ApprovalRequestID)
	if err != nil {
		return ApprovalMutationResult{}, err
	}
	if approval.Revision != command.ExpectedRevision {
		return ApprovalMutationResult{}, revisionConflict("approval request", approval.ID, command.ExpectedRevision, approval.Revision)
	}
	if approval.Status != domain.ApprovalPending {
		return ApprovalMutationResult{}, &Error{Code: CodeApprovalConflict, Message: "only a pending approval can be decided"}
	}
	action, err := querySupervisorAction(ctx, tx, workspace.ID, approval.ActionID)
	if err != nil {
		return ApprovalMutationResult{}, err
	}
	if action.Status != domain.SupervisorActionAwaitingApproval || action.Revision != approval.ExpectedActionRevision || action.ApprovalID != approval.ID {
		return ApprovalMutationResult{}, &Error{Code: CodeApprovalConflict, Message: "approval action linkage or revision is stale"}
	}
	now := s.nowText()
	status, eventType := domain.ApprovalDenied, approvalDeniedEvent
	if allow {
		status, eventType = domain.ApprovalGranted, approvalGrantedEvent
	}
	approval.Status, approval.DecisionNote, approval.Revision = status, command.DecisionNote, approval.Revision+1
	approval.UpdatedAt, approval.DecidedAt, approval.UpdatedBy, approval.DecidedBy = now, now, localOwnerActorID, localOwnerActorID
	sequence, err := appendEvent(ctx, tx, workspace.ID, "approval_request", approval.ID, approval.Revision, eventType, command.CorrelationID, now, map[string]any{"action_id": action.ID, "status": status, "decision_note": command.DecisionNote})
	if err != nil {
		return ApprovalMutationResult{}, err
	}
	approval.DecisionEventSequence = sequence
	if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status=?,decision_note=?,decision_event_sequence=?,revision=?,updated_at=?,decided_at=?,updated_by=?,decided_by=? WHERE id=?`, status, approval.DecisionNote, sequence, approval.Revision, now, now, localOwnerActorID, localOwnerActorID, approval.ID); err != nil {
		return ApprovalMutationResult{}, storageFailure("decide approval", err)
	}
	if allow {
		if err := s.applyApprovedSupervisorAction(ctx, tx, &action, command.CorrelationID, now); err != nil {
			return ApprovalMutationResult{}, err
		}
		action.Status, action.Decision, action.Revision, action.UpdatedAt, action.UpdatedBy, action.AppliedAt = domain.SupervisorActionApplied, command.DecisionNote, action.Revision+1, now, localOwnerActorID, now
		if _, err := tx.ExecContext(ctx, `UPDATE supervisor_actions SET status='applied',decision=?,revision=?,updated_at=?,applied_at=?,updated_by=? WHERE id=?`, action.Decision, action.Revision, now, now, localOwnerActorID, action.ID); err != nil {
			return ApprovalMutationResult{}, storageFailure("apply approved supervisor action", err)
		}
		if _, err := appendEvent(ctx, tx, workspace.ID, "supervisor_action", action.ID, action.Revision, supervisorActionAppliedEvent, command.CorrelationID, now, map[string]any{"approval_id": approval.ID, "response": action.Response, "decision": action.Decision}); err != nil {
			return ApprovalMutationResult{}, err
		}
		approval.Status, approval.Revision = domain.ApprovalConsumed, approval.Revision+1
		if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status='consumed',revision=?,updated_at=?,updated_by=? WHERE id=?`, approval.Revision, now, localOwnerActorID, approval.ID); err != nil {
			return ApprovalMutationResult{}, storageFailure("consume approval", err)
		}
		sequence, err = appendEvent(ctx, tx, workspace.ID, "approval_request", approval.ID, approval.Revision, approvalConsumedEvent, command.CorrelationID, now, map[string]any{"action_id": action.ID, "status": approval.Status})
		if err != nil {
			return ApprovalMutationResult{}, err
		}
	} else {
		action.Status, action.Decision, action.Revision, action.UpdatedAt, action.UpdatedBy = domain.SupervisorActionDismissed, command.DecisionNote, action.Revision+1, now, localOwnerActorID
		if _, err := tx.ExecContext(ctx, `UPDATE supervisor_actions SET status='dismissed',decision=?,revision=?,updated_at=?,updated_by=? WHERE id=?`, action.Decision, action.Revision, now, localOwnerActorID, action.ID); err != nil {
			return ApprovalMutationResult{}, storageFailure("dismiss denied supervisor action", err)
		}
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return ApprovalMutationResult{}, err
	}
	result := ApprovalMutationResult{Approval: approval, Action: action, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return ApprovalMutationResult{}, err
	}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, operation, requestHash, result, now); err != nil {
		return ApprovalMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApprovalMutationResult{}, storageFailure("commit approval decision", err)
	}
	return result, nil
}

func (s *Store) applyApprovedSupervisorAction(ctx context.Context, tx *sql.Tx, action *domain.SupervisorAction, correlationID, now string) error {
	switch action.Response {
	case domain.SupervisorResponseRequestOwner:
		// request_owner is an explicit acknowledgement decision. It changes no
		// governed target projection; the applied action and consumed approval are
		// its complete durable effect.
		return nil
	case domain.SupervisorResponseResumeRun:
		return s.applyApprovedRunResume(ctx, tx, action, correlationID, now)
	case domain.SupervisorResponseStopRun:
		return s.applyApprovedRunStop(ctx, tx, action, correlationID, now)
	case domain.SupervisorResponseRetryTask, domain.SupervisorResponseReassignTask:
		if action.Condition != domain.SupervisorConditionManagerEscalation {
			return &Error{Code: CodeApprovalConflict, Message: "automatic retry and reassignment cannot be widened by an approval"}
		}
		return s.applyApprovedTaskReschedule(ctx, tx, action, correlationID, now)
	default:
		return &Error{Code: CodeApprovalConflict, Message: "approved action has no executable M16 response"}
	}
}

func (s *Store) applyApprovedRunResume(ctx context.Context, tx *sql.Tx, action *domain.SupervisorAction, correlationID, now string) error {
	if action.RunID == "" {
		return &Error{Code: CodeApprovalConflict, Message: "approved resume lacks its exact run"}
	}
	run, err := queryRun(ctx, tx, action.WorkspaceID, action.RunID)
	if err != nil {
		return err
	}
	if run.Revision != action.EntityRevision || run.Status != domain.RunBlocked {
		return &Error{Code: CodeApprovalConflict, Message: "blocked run revision or state changed before approval"}
	}
	var jobStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM run_jobs WHERE run_id=?`, run.ID).Scan(&jobStatus); err != nil || jobStatus != "complete" {
		return &Error{Code: CodeApprovalConflict, Message: "blocked run already has pending work", Cause: err}
	}
	task, err := queryTask(ctx, tx, run.WorkspaceID, run.TaskID)
	if err != nil {
		return err
	}
	if task.AssignmentID != run.AssignmentID || task.AssignedAgentID != run.AgentID {
		return &Error{Code: CodeApprovalConflict, Message: "blocked run no longer retains its exact assignment"}
	}
	task.Status, task.BlockedReason, task.Revision = domain.TaskActive, "", task.Revision+1
	if err := updateTaskState(ctx, tx, task, now); err != nil {
		return err
	}
	run.Status, run.BlockedQuestion, run.Revision = domain.RunActive, "", run.Revision+1
	if err := updateRunProjection(ctx, tx, run, now); err != nil {
		return err
	}
	if err := setRunJob(ctx, tx, run.ID, "pending", now); err != nil {
		return err
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runResumedEvent, "run resumed by approved supervisor action", nil, now); err != nil {
		return err
	}
	_, err = appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runResumedEvent, correlationID, now, map[string]any{"supervisor_action_id": action.ID, "step_cursor": run.StepCursor})
	return err
}

func (s *Store) applyApprovedRunStop(ctx context.Context, tx *sql.Tx, action *domain.SupervisorAction, correlationID, now string) error {
	if action.RunID == "" {
		return &Error{Code: CodeApprovalConflict, Message: "approved stop lacks its exact run"}
	}
	run, err := queryRun(ctx, tx, action.WorkspaceID, action.RunID)
	if err != nil {
		return err
	}
	if run.Revision != action.EntityRevision || (run.Status != domain.RunActive && run.Status != domain.RunBlocked) {
		return &Error{Code: CodeApprovalConflict, Message: "run revision or actionable state changed before approved stop"}
	}
	run.Status, run.StopGraceMillis, run.Revision = domain.RunStopping, 30000, run.Revision+1
	if err := updateRunProjection(ctx, tx, run, now); err != nil {
		return err
	}
	if err := setRunJob(ctx, tx, run.ID, "pending", now); err != nil {
		return err
	}
	if err := appendRunTimeline(ctx, tx, run.ID, runStopRequestedEvent, "run stop requested by approved manager escalation", nil, now); err != nil {
		return err
	}
	_, err = appendEvent(ctx, tx, run.WorkspaceID, "run", run.ID, run.Revision, runStopRequestedEvent, correlationID, now, map[string]any{
		"supervisor_action_id": action.ID, "grace_period_millis": run.StopGraceMillis,
	})
	return err
}

func (s *Store) applyApprovedTaskReschedule(ctx context.Context, tx *sql.Tx, action *domain.SupervisorAction, correlationID, now string) error {
	var payloadJSON string
	if err := tx.QueryRowContext(ctx, `SELECT source.payload_json FROM manager_proposal_actions source
JOIN manager_proposals proposal ON proposal.id=source.proposal_id
WHERE source.id=? AND source.proposal_id=? AND source.type='request_action' AND proposal.status='accepted'
  AND proposal.workspace_id=?`, action.SourceActionID, action.SourceProposalID, action.WorkspaceID).Scan(&payloadJSON); err != nil {
		return &Error{Code: CodeApprovalConflict, Message: "manager escalation source is no longer exact", Cause: err}
	}
	var request domain.ProposalRequestAction
	if err := json.Unmarshal([]byte(payloadJSON), &request); err != nil || request.Response != action.Response || request.TargetTaskID != action.TaskID || request.ExpectedRevision != action.EntityRevision {
		return &Error{Code: CodeApprovalConflict, Message: "manager escalation payload differs from the approved action", Cause: err}
	}
	task, err := queryTask(ctx, tx, action.WorkspaceID, action.TaskID)
	if err != nil {
		return err
	}
	if task.Revision != action.EntityRevision {
		return &Error{Code: CodeApprovalConflict, Message: "task revision changed before approved rescheduling"}
	}
	profileID := request.LaunchProfileID
	var retryPrior *domain.Run
	if action.Response == domain.SupervisorResponseRetryTask {
		if action.PriorRunID == "" {
			return &Error{Code: CodeApprovalConflict, Message: "approved retry lacks its exact prior failed run"}
		}
		prior, err := queryRun(ctx, tx, action.WorkspaceID, action.PriorRunID)
		if err != nil {
			return err
		}
		if prior.TaskID != task.ID || prior.Status != domain.RunStartFailed || prior.StepCursor != 0 || task.Status != domain.TaskAssigned || task.AssignmentID != prior.AssignmentID {
			return &Error{Code: CodeApprovalConflict, Message: "definite failed run or retained task authority changed before retry approval"}
		}
		var latestFailureID string
		var hasRetrySuccessor int
		if err := tx.QueryRowContext(ctx, `SELECT id FROM runs WHERE task_id=? AND status='start_failed' AND step_cursor=0 ORDER BY created_at DESC,id DESC LIMIT 1`, task.ID).Scan(&latestFailureID); err != nil {
			return &Error{Code: CodeApprovalConflict, Message: "definite failed run is no longer current", Cause: err}
		}
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_retry_receipts WHERE prior_run_id=?)`, prior.ID).Scan(&hasRetrySuccessor); err != nil {
			return storageFailure("revalidate approved retry successor", err)
		}
		if latestFailureID != prior.ID || hasRetrySuccessor != 0 {
			return &Error{Code: CodeApprovalConflict, Message: "definite failed run was already superseded before retry approval"}
		}
		retryPrior = &prior
		if err := tx.QueryRowContext(ctx, `SELECT launch_profile_id FROM (
  SELECT launch_profile_id FROM run_scheduling_receipts WHERE run_id=?
  UNION ALL SELECT launch_profile_id FROM run_retry_receipts WHERE run_id=?
) LIMIT 1`, prior.ID, prior.ID).Scan(&profileID); err != nil {
			return &Error{Code: CodeApprovalConflict, Message: "failed run has no exact launch profile receipt", Cause: err}
		}
	} else if task.Status != domain.TaskReady && task.Status != domain.TaskFailed && task.Status != domain.TaskChangesRequested {
		return &Error{Code: CodeApprovalConflict, Message: "task is no longer in an actionable reassign state"}
	}
	profile, err := queryLaunchProfile(ctx, tx, action.WorkspaceID, profileID)
	if err != nil {
		return err
	}
	agent, err := queryAgent(ctx, tx, action.WorkspaceID, profile.AgentID)
	if err != nil {
		return err
	}
	if profile.Status != domain.LaunchProfileActive || profile.ProjectID != task.ProjectID || profile.ManagerGrantID != "" ||
		!agent.Enabled || agent.Revision != profile.AgentRevision || agent.Runtime != profile.Runtime || agent.Provider != profile.Provider {
		return &Error{Code: CodeApprovalConflict, Message: "approved rescheduling launch profile or agent is stale"}
	}
	var reserved, open int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE task_id=? AND status IN ('requested','starting','active','blocked','stopping','lost')`, task.ID).Scan(&reserved); err != nil {
		return storageFailure("revalidate approved rescheduling runs", err)
	}
	if reserved != 0 {
		return &Error{Code: CodeApprovalConflict, Message: "task acquired reserved run authority before approved rescheduling"}
	}
	// Close the exact old accepted intent before creating its successor. Retry
	// and reassign never revive or mutate a terminal external run operation.
	if retryPrior != nil {
		if err := terminalizeSchedulingIntentForRun(ctx, tx, *retryPrior, "superseded by approved manager escalation retry", correlationID, now); err != nil {
			return err
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduling_intents WHERE task_id=? AND status IN ('pending','deferred','awaiting_approval','run_requested')`, task.ID).Scan(&open); err != nil {
		return storageFailure("revalidate approved rescheduling intents", err)
	}
	if open != 0 {
		return &Error{Code: CodeApprovalConflict, Message: "task acquired another open scheduling intent before approval"}
	}
	if task.AssignmentID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE task_assignments SET status='released',revision=revision+1,updated_at=?,updated_by='local-owner' WHERE id=? AND status='active'`, now, task.AssignmentID); err != nil {
			return storageFailure("release superseded task assignment", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE work_claims SET status='released',revision=revision+1,updated_at=?,updated_by='local-owner' WHERE task_id=? AND status='active'`, now, task.ID); err != nil {
			return storageFailure("release superseded task claims", err)
		}
	}
	task.Status, task.AssignmentID, task.AssignedAgentID, task.AssignmentLeaseExpiresAt = domain.TaskReady, "", "", ""
	task.BlockedReason, task.Revision = "", task.Revision+1
	if err := updateTaskState(ctx, tx, task, now); err != nil {
		return err
	}
	intentID, err := randomID("sintent_")
	if err != nil {
		return storageFailure("generate approved rescheduling intent", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO scheduling_intents(id,workspace_id,project_id,objective_id,task_id,agent_id,launch_profile_id,source_proposal_id,source_action_id,status,attempts,revision,created_at,updated_at,created_by,updated_by)
VALUES (?,?,?,?,?,?,?,?,?,'pending',0,1,?,?,?,?)`, intentID, task.WorkspaceID, task.ProjectID, task.ObjectiveID, task.ID, profile.AgentID, profile.ID,
		action.SourceProposalID, action.SourceActionID, now, now, localOwnerActorID, localOwnerActorID); err != nil {
		return storageFailure("insert approved rescheduling intent", err)
	}
	if _, err := appendEvent(ctx, tx, task.WorkspaceID, "task", task.ID, task.Revision, taskReadied, correlationID, now, map[string]any{
		"supervisor_action_id": action.ID, "launch_profile_id": profile.ID, "response": action.Response, "status": domain.TaskReady,
	}); err != nil {
		return err
	}
	_, err = appendEvent(ctx, tx, task.WorkspaceID, "scheduling_intent", intentID, 1, schedulingIntentCreatedEvent, correlationID, now, map[string]any{
		"task_id": task.ID, "agent_id": profile.AgentID, "launch_profile_id": profile.ID, "source_proposal_id": action.SourceProposalID,
	})
	return err
}

const supervisorPolicySelect = `SELECT workspace_id,revision,enabled,max_active_runs,max_starting_runs,default_project_concurrency,default_provider_concurrency,project_concurrency_json,provider_concurrency_json,auto_schedule,auto_retry_limit,retry_cooldown_seconds,created_at,updated_at,created_by,updated_by FROM supervisor_policies`

func querySupervisorPolicy(ctx context.Context, database queryRower, workspaceID string) (domain.SupervisorPolicy, error) {
	var value domain.SupervisorPolicy
	var projectsJSON, providersJSON string
	err := database.QueryRowContext(ctx, supervisorPolicySelect+` WHERE workspace_id=? ORDER BY revision DESC LIMIT 1`, workspaceID).Scan(
		&value.WorkspaceID, &value.Revision, &value.Enabled, &value.Limits.MaxActiveRuns, &value.Limits.MaxStartingRuns,
		&value.Limits.DefaultProjectConcurrency, &value.Limits.DefaultProviderConcurrency, &projectsJSON, &providersJSON,
		&value.AutoSchedule, &value.AutoRetryLimit, &value.RetryCooldownSeconds, &value.CreatedAt, &value.UpdatedAt, &value.CreatedBy, &value.UpdatedBy)
	if err != nil {
		return domain.SupervisorPolicy{}, storageFailure("query supervisor policy", err)
	}
	if err := json.Unmarshal([]byte(projectsJSON), &value.Limits.ProjectConcurrency); err != nil {
		return domain.SupervisorPolicy{}, storageFailure("decode supervisor project limits", err)
	}
	if err := json.Unmarshal([]byte(providersJSON), &value.Limits.ProviderConcurrency); err != nil {
		return domain.SupervisorPolicy{}, storageFailure("decode supervisor provider limits", err)
	}
	return value, nil
}

func validSupervisorLimits(value domain.SupervisorLimits) bool {
	return value.MaxActiveRuns >= 1 && value.MaxActiveRuns <= 100 && value.MaxStartingRuns >= 1 && value.MaxStartingRuns <= value.MaxActiveRuns &&
		value.DefaultProjectConcurrency >= 1 && value.DefaultProjectConcurrency <= 100 && value.DefaultProviderConcurrency >= 1 && value.DefaultProviderConcurrency <= 100 &&
		len(value.ProjectConcurrency) <= 100 && len(value.ProviderConcurrency) <= 100
}

func canonicalConcurrencyMap(values map[string]int, projects bool) (map[string]int, error) {
	result := make(map[string]int, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" || value < 1 || value > 100 || len(key) > 128 {
			return nil, &Error{Code: CodeInvalidSupervisorPolicy, Message: "supervisor concurrency override is invalid"}
		}
		result[key] = value
	}
	return result, nil
}

func validateProposalAuthority(ctx context.Context, tx *sql.Tx, run domain.Run, grant domain.ManagerGrant, command SubmitManagerProposalCommand, now string) error {
	if grant.Status != domain.ManagerGrantActive || grant.AgentID != run.AgentID || grant.TaskID != run.TaskID || grant.ProjectID != run.ProjectID ||
		grant.Revision != command.ExpectedGrantRevision || !containsString(grant.ProposalKinds, command.Kind) ||
		run.Status != domain.RunStarting && run.Status != domain.RunActive && run.Status != domain.RunBlocked {
		return &Error{Code: CodeManagerProposalDenied, Message: "run is not authorized by the current exact manager grant"}
	}
	if grant.ExpiresAt != "" {
		expires, err := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
		observed, observedErr := time.Parse(time.RFC3339Nano, now)
		if err != nil || observedErr != nil || !expires.After(observed) {
			return &Error{Code: CodeManagerProposalDenied, Message: "manager grant has expired"}
		}
	}
	var packetSchema, packetGrantID string
	var packetGrantRevision, packetAsOf int64
	if err := tx.QueryRowContext(ctx, `
SELECT json_extract(packet.packet_json,'$.schema'),
       json_extract(packet.packet_json,'$.management_grant.grant_id'),
       json_extract(packet.packet_json,'$.management_grant.grant_revision'),
       json_extract(packet.packet_json,'$.as_of_event_sequence')
FROM run_context_bindings binding JOIN context_packets packet ON packet.id=binding.context_packet_id
JOIN run_capabilities capability ON capability.run_id=binding.run_id
WHERE binding.run_id=? AND crewfold_timestamp_key(capability.expires_at)>crewfold_timestamp_key(?)`, run.ID, now).Scan(&packetSchema, &packetGrantID, &packetGrantRevision, &packetAsOf); err != nil {
		return &Error{Code: CodeManagerProposalDenied, Message: "run has no current manager capability", Cause: err}
	}
	if packetSchema != domain.ContextPacketSchema || packetGrantID != grant.ID || packetGrantRevision != grant.Revision || command.AsOfEventSequence != packetAsOf {
		return &Error{Code: CodeManagerProposalDenied, Message: "run packet does not carry the current exact manager grant"}
	}
	var open int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM manager_proposals WHERE grant_id=? AND status IN ('pending','invalid')", grant.ID).Scan(&open); err != nil {
		return storageFailure("count open manager proposals", err)
	}
	if open >= grant.Limits.MaxOpenProposals {
		return &Error{Code: CodeManagerProposalDenied, Message: "manager grant open proposal limit reached"}
	}
	return nil
}

func (s *Store) validateManagerProposalInTransaction(ctx context.Context, tx *sql.Tx, grant domain.ManagerGrant, kind string, actions []domain.ManagerProposalAction) ([]domain.ProposalValidationIssue, error) {
	issues := make([]domain.ProposalValidationIssue, 0)
	add := func(code, path, message string) {
		if len(issues) < maximumManagerValidationIssues {
			issues = append(issues, domain.ProposalValidationIssue{Code: code, Path: path, Message: message, Severity: domain.ProposalIssueError})
		}
	}
	counts := map[string]int{}
	keys := map[string]bool{}
	budget := domain.Budget{}
	checkRef := func(ref domain.ProposalTaskRef, proposedAllowed bool, path string) bool {
		if ref.TaskID == "" {
			if proposedAllowed && ref.ExpectedTaskRevision == 0 && ref.ProposalTaskKey != "" && keys[ref.ProposalTaskKey] {
				return true
			}
			add("invalid_task_ref", path, "proposed task reference is not defined earlier in this proposal")
			return false
		}
		if ref.ProposalTaskKey != "" || ref.ExpectedTaskRevision < 1 {
			add("invalid_task_ref", path, "existing task reference requires only task_id and expected_task_revision")
			return false
		}
		task, err := queryTask(ctx, tx, grant.WorkspaceID, ref.TaskID)
		if err != nil || task.ProjectID != grant.ProjectID || task.ObjectiveID != grant.ObjectiveID || task.Revision != ref.ExpectedTaskRevision {
			add("task_scope_or_revision", path, "existing task is outside grant scope or its revision is stale")
			return false
		}
		return true
	}
	checkProfile := func(profileID, path string) bool {
		frozen, allowed := grantProfile(grant, profileID)
		if !allowed {
			add("profile_not_allowed", path, "launch profile is outside the exact grant")
			return false
		}
		profile, err := queryLaunchProfile(ctx, tx, grant.WorkspaceID, profileID)
		if err != nil || profile.Status != domain.LaunchProfileActive || profile.Revision != frozen.Revision ||
			profile.AgentID != frozen.AgentID || profile.AgentRevision != frozen.AgentRevision || profile.ManagerGrantID != "" {
			add("profile_stale", path, "launch profile is retired or differs from the frozen grant tuple")
			return false
		}
		agent, err := queryAgent(ctx, tx, grant.WorkspaceID, profile.AgentID)
		if err != nil || !agent.Enabled || agent.Revision != profile.AgentRevision ||
			agent.Runtime != profile.Runtime || agent.Provider != profile.Provider {
			add("profile_agent_stale", path, "launch profile agent is disabled or differs from the frozen profile revision")
			return false
		}
		return true
	}
	for index, action := range actions {
		path := fmt.Sprintf("actions[%d]", index)
		payload, err := proposalActionPayload(action)
		if err != nil {
			add("invalid_action_union", path, err.Error())
			continue
		}
		_ = payload
		counts[action.Type]++
		switch action.Type {
		case domain.ProposalActionCreateTask:
			value := action.CreateTask
			if !validManagerTaskKey(value.TaskKey) || keys[value.TaskKey] || !validTitle(strings.TrimSpace(value.Title)) || len(strings.TrimSpace(value.Description)) > 4096 || value.Priority < 0 || value.Priority > 1000 || !validBudget(value.Budget) {
				add("invalid_task", path, "created task fields or task_key are invalid")
			}
			keys[value.TaskKey] = true
			checkProfile(value.LaunchProfileID, path+".launch_profile_id")
			var overflow bool
			budget, overflow = addBudgetChecked(budget, value.Budget)
			if overflow {
				add("budget_overflow", path+".budget", "proposal budget arithmetic overflowed")
			}
		case domain.ProposalActionAddDependency:
			checkRef(action.AddDependency.Task, true, path+".task")
			checkRef(action.AddDependency.DependsOn, true, path+".depends_on")
		case domain.ProposalActionDeclareClaimRequirement:
			value := action.DeclareClaimRequirement
			normalized, err := domain.NormalizeClaimTarget(value.Kind, value.Target)
			if err != nil || normalized != value.Target || !containsString(grant.AllowedClaimKinds, value.Kind) || !domain.ValidClaimMode(value.Mode) || !domain.ValidClaimPolicy(value.ConflictPolicy) || !checkRef(value.Task, true, path+".task") {
				add("invalid_claim_requirement", path, "claim requirement is invalid or outside the grant")
			}
		case domain.ProposalActionAssignTask:
			if checkRef(action.AssignTask.Task, false, path+".task") {
				task, err := queryTask(ctx, tx, grant.WorkspaceID, action.AssignTask.Task.TaskID)
				var openIntentCount int
				if err == nil {
					err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduling_intents WHERE task_id=? AND status IN ('pending','deferred','awaiting_approval','run_requested')`, task.ID).Scan(&openIntentCount)
				}
				readiness := domain.TaskReadiness{}
				if err == nil {
					readiness, err = taskReadiness(ctx, tx, task)
				}
				if err != nil {
					return nil, err
				}
				if task.Status != domain.TaskReady || task.AssignmentID != "" || !readiness.Ready || openIntentCount != 0 {
					add("task_not_assignable", path+".task", "assignment target must be dependency-ready, unassigned, and have no open scheduling intent")
				}
			}
			checkProfile(action.AssignTask.LaunchProfileID, path+".launch_profile_id")
		case domain.ProposalActionRequestReview:
			value := action.RequestReview
			validTarget := checkRef(value.Task, false, path+".task")
			if !validTarget || !validTitle(strings.TrimSpace(value.Title)) || len(strings.TrimSpace(value.Description)) > 4096 || value.Priority < 0 || value.Priority > 1000 || !validBudget(value.Budget) {
				add("invalid_review", path, "review request fields are invalid")
			}
			validProfile := checkProfile(value.LaunchProfileID, path+".launch_profile_id")
			if validTarget && validProfile {
				target, err := queryTask(ctx, tx, grant.WorkspaceID, value.Task.TaskID)
				if err != nil {
					return nil, err
				}
				frozen, _ := grantProfile(grant, value.LaunchProfileID)
				executionAgentID := target.AssignedAgentID
				if executionAgentID == "" {
					if err := tx.QueryRowContext(ctx, `SELECT agent_id FROM scheduling_intents WHERE task_id=? AND status IN ('pending','deferred','awaiting_approval','run_requested') ORDER BY created_at,id LIMIT 1`, target.ID).Scan(&executionAgentID); err != nil && !errors.Is(err, sql.ErrNoRows) {
						return nil, storageFailure("resolve review target scheduling agent", err)
					}
				}
				if executionAgentID == "" {
					if err := tx.QueryRowContext(ctx, `SELECT agent_id FROM runs WHERE task_id=? ORDER BY created_at DESC,id DESC LIMIT 1`, target.ID).Scan(&executionAgentID); err != nil && !errors.Is(err, sql.ErrNoRows) {
						return nil, storageFailure("resolve review target execution agent", err)
					}
				}
				if executionAgentID == "" {
					add("review_implementer_unknown", path+".task", "review target must have one exact current, planned, or historical execution agent before an independent reviewer can be fixed")
				} else if frozen.AgentID == executionAgentID {
					add("reviewer_not_independent", path+".launch_profile_id", "review launch profile must fix an agent different from the target task execution agent")
				}
			}
			var overflow bool
			budget, overflow = addBudgetChecked(budget, value.Budget)
			if overflow {
				add("budget_overflow", path+".budget", "proposal budget arithmetic overflowed")
			}
		case domain.ProposalActionRequestAction:
			value := action.RequestAction
			exactTarget := false
			switch value.Response {
			case domain.ProposalResponseResumeRun, domain.ProposalResponseStopRun:
				exactTarget = value.TargetRunID != "" && value.TargetTaskID == "" && value.LaunchProfileID == ""
				if exactTarget {
					run, err := queryRun(ctx, tx, grant.WorkspaceID, value.TargetRunID)
					if err == nil {
						task, taskErr := queryTask(ctx, tx, grant.WorkspaceID, run.TaskID)
						exactTarget = taskErr == nil && run.ProjectID == grant.ProjectID && task.ObjectiveID == grant.ObjectiveID &&
							run.Revision == value.ExpectedRevision
						if value.Response == domain.ProposalResponseResumeRun {
							var jobStatus string
							jobErr := tx.QueryRowContext(ctx, `SELECT status FROM run_jobs WHERE run_id=?`, run.ID).Scan(&jobStatus)
							exactTarget = exactTarget && run.Status == domain.RunBlocked && jobErr == nil && jobStatus == "complete" &&
								task.AssignmentID == run.AssignmentID && task.AssignedAgentID == run.AgentID
						} else {
							exactTarget = exactTarget && (run.Status == domain.RunActive || run.Status == domain.RunBlocked)
						}
					}
				}
			case domain.ProposalResponseRetryTask:
				exactTarget = value.TargetTaskID != "" && value.TargetRunID == "" && value.LaunchProfileID == ""
				if exactTarget {
					task, err := queryTask(ctx, tx, grant.WorkspaceID, value.TargetTaskID)
					exactTarget = err == nil && task.ProjectID == grant.ProjectID && task.ObjectiveID == grant.ObjectiveID && task.Revision == value.ExpectedRevision
					if exactTarget {
						var runID string
						var runRevision int64
						var runStatus, jobStatus string
						runErr := tx.QueryRowContext(ctx, `SELECT run.id,run.revision,run.status,job.status FROM runs run JOIN run_jobs job ON job.run_id=run.id
WHERE run.task_id=? ORDER BY run.created_at DESC,run.id DESC LIMIT 1`, task.ID).Scan(&runID, &runRevision, &runStatus, &jobStatus)
						exactTarget = runErr == nil && runStatus == domain.RunStartFailed && jobStatus == "complete" &&
							task.Status == domain.TaskAssigned && task.AssignmentID != "" && runRevision > 0
					}
				}
			case domain.ProposalResponseReassignTask:
				exactTarget = value.TargetTaskID != "" && value.TargetRunID == "" && value.LaunchProfileID != "" && checkProfile(value.LaunchProfileID, path+".launch_profile_id")
				if exactTarget {
					task, err := queryTask(ctx, tx, grant.WorkspaceID, value.TargetTaskID)
					exactTarget = err == nil && task.ProjectID == grant.ProjectID && task.ObjectiveID == grant.ObjectiveID && task.Revision == value.ExpectedRevision &&
						(task.Status == domain.TaskReady || task.Status == domain.TaskFailed || task.Status == domain.TaskChangesRequested)
					if exactTarget {
						var reservedRuns, openIntents int
						if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE task_id=? AND status IN ('requested','starting','active','blocked','stopping','lost')`, task.ID).Scan(&reservedRuns); err != nil {
							return nil, storageFailure("validate escalation reassign runs", err)
						}
						if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduling_intents WHERE task_id=? AND status IN ('pending','deferred','awaiting_approval','run_requested')`, task.ID).Scan(&openIntents); err != nil {
							return nil, storageFailure("validate escalation reassign intent", err)
						}
						exactTarget = reservedRuns == 0 && openIntents == 0
					}
				}
			}
			if !validProposalResponse(value.Response) || !validManagerText(strings.TrimSpace(value.Reason), 1024) || value.ExpectedRevision < 1 || !exactTarget {
				add("invalid_escalation", path, "requested action response, target revision, or reason is invalid")
			}
		}
	}
	if len(actions) > grant.Limits.MaxActions || counts[domain.ProposalActionCreateTask] > grant.Limits.MaxTasks || counts[domain.ProposalActionAddDependency] > grant.Limits.MaxDependencies || counts[domain.ProposalActionDeclareClaimRequirement] > grant.Limits.MaxClaimRequirements {
		add("proposal_limit_exceeded", "actions", "proposal exceeds its exact owner grant count limit")
	}
	if budgetExceeds(budget, grant.Limits.Budget) {
		add("grant_budget_exceeded", "actions", "proposal exceeds its grant budget")
	}
	switch kind {
	case domain.ManagerProposalTaskDecomposition:
		if counts[domain.ProposalActionCreateTask] < 1 || counts[domain.ProposalActionAssignTask]+counts[domain.ProposalActionRequestReview]+counts[domain.ProposalActionRequestAction] != 0 {
			add("kind_action_mismatch", "actions", "task decomposition requires create/dependency/claim actions only")
		}
	case domain.ManagerProposalAssignment:
		if len(actions) != 1 || counts[domain.ProposalActionAssignTask] != 1 {
			add("kind_action_mismatch", "actions", "assignment requires exactly one assign_task action")
		}
	case domain.ManagerProposalReview:
		if len(actions) != 1 || counts[domain.ProposalActionRequestReview] != 1 {
			add("kind_action_mismatch", "actions", "review requires exactly one request_review action")
		}
	case domain.ManagerProposalEscalation:
		if len(actions) != 1 || counts[domain.ProposalActionRequestAction] != 1 {
			add("kind_action_mismatch", "actions", "escalation requires exactly one request_action action")
		}
	}
	aggregateIssues, err := s.validateManagerProposalAggregate(ctx, tx, grant, actions)
	if err != nil {
		return nil, err
	}
	issues = append(issues, aggregateIssues...)
	// Errors are authority-denying. Keep them ahead of warnings before applying
	// the public bound so a large set of overlap warnings can never hide a later
	// cycle, budget, scope, or stale-authority error.
	ordered := make([]domain.ProposalValidationIssue, 0, maximumManagerValidationIssues)
	for _, severity := range []string{domain.ProposalIssueError, domain.ProposalIssueWarning} {
		for _, issue := range issues {
			if issue.Severity == severity && len(ordered) < maximumManagerValidationIssues {
				ordered = append(ordered, issue)
			}
		}
	}
	return ordered, nil
}

func proposalActionPayload(action domain.ManagerProposalAction) (any, error) {
	count := 0
	for _, present := range []bool{action.CreateTask != nil, action.AddDependency != nil, action.DeclareClaimRequirement != nil, action.AssignTask != nil, action.RequestReview != nil, action.RequestAction != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return nil, &Error{Code: CodeInvalidManagerProposal, Message: "proposal action must contain exactly one payload"}
	}
	switch action.Type {
	case domain.ProposalActionCreateTask:
		if action.CreateTask != nil {
			return action.CreateTask, nil
		}
	case domain.ProposalActionAddDependency:
		if action.AddDependency != nil {
			return action.AddDependency, nil
		}
	case domain.ProposalActionDeclareClaimRequirement:
		if action.DeclareClaimRequirement != nil {
			return action.DeclareClaimRequirement, nil
		}
	case domain.ProposalActionAssignTask:
		if action.AssignTask != nil {
			return action.AssignTask, nil
		}
	case domain.ProposalActionRequestReview:
		if action.RequestReview != nil {
			return action.RequestReview, nil
		}
	case domain.ProposalActionRequestAction:
		if action.RequestAction != nil {
			return action.RequestAction, nil
		}
	}
	return nil, &Error{Code: CodeInvalidManagerProposal, Message: "proposal action type does not match its payload"}
}

func queryManagerGrant(ctx context.Context, database queryRower, workspaceID, grantID string) (domain.ManagerGrant, error) {
	grant, err := scanManagerGrant(database.QueryRowContext(ctx, managerGrantSelect+" WHERE workspace_id=? AND id=?", workspaceID, grantID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ManagerGrant{}, &Error{Code: CodeManagerGrantNotFound, Message: fmt.Sprintf("manager grant %q was not found", grantID)}
	}
	return grant, err
}

const managerGrantSelect = `SELECT id,workspace_id,project_id,objective_id,objective_revision,task_id,task_revision,agent_id,agent_revision,
proposal_kinds_json,launch_profiles_json,allowed_claim_kinds_json,max_open_proposals,max_actions,max_tasks,max_dependencies,
max_claim_requirements,budget_tokens,budget_cost_cents,budget_time_seconds,content_sha256,COALESCE(expires_at,''),status,revision,
created_at,updated_at,created_by,updated_by FROM manager_grants`

func scanManagerGrant(row rowScanner) (domain.ManagerGrant, error) {
	var value domain.ManagerGrant
	var proposalKindsJSON, profilesJSON, claimKindsJSON string
	err := row.Scan(&value.ID, &value.WorkspaceID, &value.ProjectID, &value.ObjectiveID, &value.ObjectiveRevision, &value.TaskID, &value.TaskRevision,
		&value.AgentID, &value.AgentRevision, &proposalKindsJSON, &profilesJSON, &claimKindsJSON,
		&value.Limits.MaxOpenProposals, &value.Limits.MaxActions, &value.Limits.MaxTasks, &value.Limits.MaxDependencies,
		&value.Limits.MaxClaimRequirements, &value.Limits.Budget.TokenLimit, &value.Limits.Budget.CostCents,
		&value.Limits.Budget.TimeSeconds, &value.ContentSHA256, &value.ExpiresAt, &value.Status, &value.Revision,
		&value.CreatedAt, &value.UpdatedAt, &value.CreatedBy, &value.UpdatedBy)
	if err != nil {
		return domain.ManagerGrant{}, err
	}
	if json.Unmarshal([]byte(proposalKindsJSON), &value.ProposalKinds) != nil || json.Unmarshal([]byte(profilesJSON), &value.LaunchProfiles) != nil || json.Unmarshal([]byte(claimKindsJSON), &value.AllowedClaimKinds) != nil {
		return domain.ManagerGrant{}, storageFailure("decode manager grant authority", errors.New("invalid authority JSON"))
	}
	return value, nil
}

func queryLaunchProfile(ctx context.Context, database queryRower, workspaceID, profileID string) (domain.LaunchProfile, error) {
	value, err := scanLaunchProfile(database.QueryRowContext(ctx, launchProfileSelect+" WHERE workspace_id=? AND id=?", workspaceID, profileID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.LaunchProfile{}, &Error{Code: CodeLaunchProfileNotFound, Message: fmt.Sprintf("launch profile %q was not found", profileID)}
	}
	return value, err
}

const schedulingIntentSelect = `SELECT id,workspace_id,project_id,objective_id,task_id,agent_id,launch_profile_id,COALESCE(source_proposal_id,''),COALESCE(source_action_id,''),COALESCE(source_check_repair_proposal_id,''),status,COALESCE(reason,''),COALESCE(assignment_id,''),COALESCE(run_id,''),COALESCE(supervisor_action_id,''),attempts,last_evaluated_event_sequence,revision,created_at,updated_at,COALESCE(next_attempt_at,''),created_by,updated_by FROM scheduling_intents`

func querySchedulingIntent(ctx context.Context, database queryRower, workspaceID, intentID string) (domain.SchedulingIntent, error) {
	value, err := scanSchedulingIntent(database.QueryRowContext(ctx, schedulingIntentSelect+` WHERE workspace_id=? AND id=?`, workspaceID, intentID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SchedulingIntent{}, &Error{Code: CodeSupervisorActionNotFound, Message: fmt.Sprintf("scheduling intent %q was not found", intentID)}
	}
	if err != nil {
		return domain.SchedulingIntent{}, storageFailure("query scheduling intent", err)
	}
	return value, nil
}

func terminalSchedulingIntentStatusForRun(status string) (string, string, bool) {
	switch status {
	case domain.RunCompleted:
		return domain.SchedulingIntentSatisfied, schedulingIntentSatisfiedEvent, true
	case domain.RunReview, domain.RunFailed, domain.RunStartFailed:
		return domain.SchedulingIntentFailed, schedulingIntentFailedEvent, true
	case domain.RunStopped:
		return domain.SchedulingIntentCancelled, schedulingIntentCancelledEvent, true
	default:
		return "", "", false
	}
}

// terminalizeStrandedRetryIntentsForPolicy closes open intents whose latest
// definite start failure can no longer receive a policy-authorized successor.
// It deliberately resolves the latest run through the immutable retry receipt
// chain; an earlier failed attempt never closes an intent after a successor was
// already sealed.
func terminalizeStrandedRetryIntentsForPolicy(ctx context.Context, tx *sql.Tx, policy domain.SupervisorPolicy, correlationID, now string) error {
	type retryCandidate struct {
		runID      string
		retryCount int
	}
	rows, err := tx.QueryContext(ctx, `SELECT
  COALESCE((
    SELECT receipt.run_id FROM run_retry_receipts receipt
    WHERE receipt.intent_id=intent.id
    ORDER BY receipt.attempt DESC,receipt.run_id DESC LIMIT 1
  ),intent.run_id) AS latest_run_id,
  (SELECT COUNT(*) FROM run_retry_receipts receipt WHERE receipt.intent_id=intent.id) AS retry_count
FROM scheduling_intents intent
WHERE intent.workspace_id=? AND intent.status='run_requested'
ORDER BY intent.created_at,intent.id`, policy.WorkspaceID)
	if err != nil {
		return storageFailure("scan retry intents after policy change", err)
	}
	candidates := make([]retryCandidate, 0)
	for rows.Next() {
		var candidate retryCandidate
		if err := rows.Scan(&candidate.runID, &candidate.retryCount); err != nil {
			rows.Close()
			return storageFailure("scan retry intent after policy change", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Close(); err != nil {
		return storageFailure("close retry intent policy scan", err)
	}
	for _, candidate := range candidates {
		// An enabled policy with unused retry capacity still owns this intent.
		if policy.Enabled && candidate.retryCount < policy.AutoRetryLimit {
			continue
		}
		run, err := queryRun(ctx, tx, policy.WorkspaceID, candidate.runID)
		if err != nil {
			return err
		}
		if run.Status != domain.RunStartFailed || run.StepCursor != 0 {
			continue
		}
		var successorExists int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_retry_receipts WHERE prior_run_id=?)`, run.ID).Scan(&successorExists); err != nil {
			return storageFailure("validate latest retry attempt after policy change", err)
		}
		if successorExists != 0 {
			continue
		}
		reason := "automatic retry is disabled by the current supervisor policy"
		if policy.Enabled {
			reason = fmt.Sprintf("automatic retry limit %d is exhausted by %d prior retries", policy.AutoRetryLimit, candidate.retryCount)
		}
		if err := terminalizeSchedulingIntentForRun(ctx, tx, run, reason, correlationID, now); err != nil {
			return err
		}
	}
	return nil
}

// terminalizeSchedulingIntentForRun closes the one accepted scheduling intent
// that authorized a definitive run outcome. Fresh retries retain the original
// intent ID through run_retry_receipts, so only the final successor closes it.
// Runs outside supervisor scheduling have no matching intent and are a no-op.
func terminalizeSchedulingIntentForRun(ctx context.Context, tx *sql.Tx, run domain.Run, reason, correlationID, now string) error {
	status, eventType, terminal := terminalSchedulingIntentStatusForRun(run.Status)
	if !terminal {
		return nil
	}
	intent, err := scanSchedulingIntent(tx.QueryRowContext(ctx, schedulingIntentSelect+` WHERE workspace_id=? AND status='run_requested' AND (
run_id=? OR id=(SELECT intent_id FROM run_retry_receipts WHERE run_id=?)) ORDER BY created_at,id LIMIT 1`, run.WorkspaceID, run.ID, run.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return storageFailure("query terminal scheduling intent", err)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "run " + run.Status
	}
	if len(reason) > 1024 {
		reason = reason[:1024]
	}
	nextRevision := intent.Revision + 1
	if _, err := appendEventForActor(ctx, tx, run.WorkspaceID, "scheduling_intent", intent.ID, nextRevision, eventType,
		correlationID, now, "subsystem:supervisor", "subsystem", map[string]any{
			"run_id": run.ID, "run_status": run.Status, "status": status, "reason": reason,
		}); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE scheduling_intents SET status=?,reason=?,revision=?,updated_at=?,updated_by='subsystem:supervisor' WHERE id=? AND status='run_requested'`,
		status, reason, nextRevision, now, intent.ID); err != nil {
		return storageFailure("terminalize scheduling intent", err)
	}
	return nil
}

const launchProfileSelect = `SELECT id,workspace_id,project_id,agent_id,agent_revision,COALESCE(purpose,''),runtime,provider,
COALESCE(checkout_id,''),scenario_json,scenario_sha256,content_sha256,assignment_lease_seconds,capability_ttl_seconds,
COALESCE(manager_grant_id,''),status,revision,created_at,updated_at,created_by,updated_by FROM launch_profiles`

func scanLaunchProfile(row rowScanner) (domain.LaunchProfile, error) {
	var value domain.LaunchProfile
	var scenarioJSON string
	err := row.Scan(&value.ID, &value.WorkspaceID, &value.ProjectID, &value.AgentID, &value.AgentRevision, &value.Purpose,
		&value.Runtime, &value.Provider, &value.CheckoutID, &scenarioJSON, &value.ScenarioSHA256, &value.ContentSHA256,
		&value.AssignmentLeaseSeconds, &value.CapabilityTTLSeconds, &value.ManagerGrantID, &value.Status, &value.Revision,
		&value.CreatedAt, &value.UpdatedAt, &value.CreatedBy, &value.UpdatedBy)
	if err != nil {
		return domain.LaunchProfile{}, err
	}
	if err := json.Unmarshal([]byte(scenarioJSON), &value.Scenario); err != nil {
		return domain.LaunchProfile{}, storageFailure("decode launch profile scenario", err)
	}
	return value, nil
}

const managerProposalSelect = `SELECT id,workspace_id,project_id,objective_id,objective_revision,source_run_id,source_agent_id,grant_id,grant_revision,
kind,summary,status,as_of_event_sequence,actions_json,validation_issues_json,content_sha256,COALESCE(decision_note,''),revision,
created_at,updated_at,COALESCE(decided_at,''),created_by,updated_by,COALESCE(decided_by,'') FROM manager_proposals`

func queryManagerProposal(ctx context.Context, database queryRower, workspaceID, proposalID string) (domain.ManagerProposal, error) {
	value, err := scanManagerProposal(database.QueryRowContext(ctx, managerProposalSelect+` WHERE workspace_id=? AND id=? AND EXISTS (
SELECT 1 FROM manager_proposal_submissions receipt WHERE receipt.proposal_id=manager_proposals.id)
AND (status IN ('pending','invalid') OR EXISTS (
SELECT 1 FROM manager_proposal_decisions decision WHERE decision.proposal_id=manager_proposals.id AND decision.status=manager_proposals.status
  AND decision.effect_count=(SELECT COUNT(*) FROM manager_proposal_effects effect WHERE effect.proposal_id=manager_proposals.id)
  AND decision.effects_sha256=lower(hex(sha256(CAST(COALESCE((
    SELECT json_group_array(json_object('action_id',action_id,'effect_type',effect_type,'entity_type',entity_type,'entity_id',entity_id,'event_sequence',event_sequence))
    FROM (SELECT * FROM manager_proposal_effects WHERE proposal_id=manager_proposals.id ORDER BY action_id,effect_type,entity_type,entity_id)
  ),'[]') AS BLOB))))
))`, workspaceID, proposalID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ManagerProposal{}, &Error{Code: CodeManagerProposalNotFound, Message: fmt.Sprintf("manager proposal %q was not found or is not sealed", proposalID)}
	}
	return value, err
}

func scanManagerProposal(row rowScanner) (domain.ManagerProposal, error) {
	var value domain.ManagerProposal
	var actionsJSON, issuesJSON string
	err := row.Scan(&value.ID, &value.WorkspaceID, &value.ProjectID, &value.ObjectiveID, &value.ObjectiveRevision, &value.SourceRunID, &value.SourceAgentID,
		&value.GrantID, &value.GrantRevision, &value.Kind, &value.Summary, &value.Status, &value.AsOfEventSequence,
		&actionsJSON, &issuesJSON, &value.ContentSHA256, &value.DecisionNote, &value.Revision, &value.CreatedAt, &value.UpdatedAt,
		&value.DecidedAt, &value.CreatedBy, &value.UpdatedBy, &value.DecidedBy)
	if err != nil {
		return domain.ManagerProposal{}, err
	}
	if err := json.Unmarshal([]byte(actionsJSON), &value.Actions); err != nil {
		return domain.ManagerProposal{}, storageFailure("decode manager proposal actions", err)
	}
	if err := json.Unmarshal([]byte(issuesJSON), &value.ValidationIssues); err != nil {
		return domain.ManagerProposal{}, storageFailure("decode manager proposal issues", err)
	}
	_, contentHash, err := canonicalContent(managerProposalContent{
		WorkspaceID: value.WorkspaceID, ProjectID: value.ProjectID, ObjectiveID: value.ObjectiveID, ObjectiveRevision: value.ObjectiveRevision,
		SourceRunID: value.SourceRunID, SourceAgentID: value.SourceAgentID, GrantID: value.GrantID,
		GrantRevision: value.GrantRevision, Kind: value.Kind, Summary: value.Summary,
		AsOfEventSequence: value.AsOfEventSequence, Actions: value.Actions,
	})
	if err != nil || contentHash != value.ContentSHA256 {
		return domain.ManagerProposal{}, storageFailure("validate manager proposal content", errors.New("manager proposal content hash differs"))
	}
	return value, nil
}

func canonicalContent(value any) ([]byte, string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(data)
	return data, hex.EncodeToString(digest[:]), nil
}

// Management idempotency identifies the semantic owner/agent command. Request
// correlation and the key used to locate the receipt are journal metadata and
// deliberately do not participate, so a public retry can have a fresh request
// ID and still replay the original committed result.
func hashManagementCommand(name string, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var semantic map[string]any
	if err := json.Unmarshal(encoded, &semantic); err != nil {
		return "", err
	}
	delete(semantic, "IdempotencyKey")
	delete(semantic, "CorrelationID")
	return hashCommand(name, semantic)
}

func canonicalProposalEffectsJSON(values []domain.ManagerProposalEffect) ([]byte, error) {
	copy := append([]domain.ManagerProposalEffect(nil), values...)
	sort.Slice(copy, func(i, j int) bool {
		left, right := copy[i], copy[j]
		if left.ActionID != right.ActionID {
			return left.ActionID < right.ActionID
		}
		if left.EffectType != right.EffectType {
			return left.EffectType < right.EffectType
		}
		if left.EntityType != right.EntityType {
			return left.EntityType < right.EntityType
		}
		return left.EntityID < right.EntityID
	})
	type receiptEffect struct {
		ActionID      string `json:"action_id"`
		EffectType    string `json:"effect_type"`
		EntityType    string `json:"entity_type"`
		EntityID      string `json:"entity_id"`
		EventSequence int64  `json:"event_sequence"`
	}
	items := make([]receiptEffect, 0, len(copy))
	for _, value := range copy {
		items = append(items, receiptEffect{ActionID: value.ActionID, EffectType: value.EffectType, EntityType: value.EntityType, EntityID: value.EntityID, EventSequence: value.EventSequence})
	}
	return json.Marshal(items)
}

type supervisorActionContent struct {
	ConditionKey      string         `json:"condition_key"`
	Condition         string         `json:"condition"`
	Response          string         `json:"response"`
	EntityRevision    int64          `json:"entity_revision"`
	PolicyRevision    int64          `json:"policy_revision"`
	AsOfEventSequence int64          `json:"as_of_event_sequence"`
	Reasons           []string       `json:"reasons"`
	Constraints       map[string]any `json:"constraints"`
}

type supervisorConditionIdentity struct {
	Condition        string `json:"condition"`
	Response         string `json:"response"`
	IntentID         string `json:"intent_id"`
	TaskID           string `json:"task_id"`
	RunID            string `json:"run_id"`
	PriorRunID       string `json:"prior_run_id"`
	SourceProposalID string `json:"source_proposal_id"`
	SourceActionID   string `json:"source_action_id"`
	EntityRevision   int64  `json:"entity_revision"`
}

func supervisorConditionKey(condition, response, intentID, taskID, runID string, entityRevision int64) string {
	return supervisorConditionKeyForAction(domain.SupervisorAction{
		Condition: condition, Response: response, IntentID: intentID,
		TaskID: taskID, RunID: runID, EntityRevision: entityRevision,
	})
}

func supervisorConditionKeyForAction(action domain.SupervisorAction) string {
	data, _ := json.Marshal(struct {
		Condition        string `json:"condition"`
		Response         string `json:"response"`
		IntentID         string `json:"intent_id"`
		TaskID           string `json:"task_id"`
		RunID            string `json:"run_id"`
		PriorRunID       string `json:"prior_run_id"`
		SourceProposalID string `json:"source_proposal_id"`
		SourceActionID   string `json:"source_action_id"`
		EntityRevision   int64  `json:"entity_revision"`
	}{action.Condition, action.Response, action.IntentID, action.TaskID, action.RunID,
		action.PriorRunID, action.SourceProposalID, action.SourceActionID, action.EntityRevision})
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func newSupervisorAction(policy domain.SupervisorPolicy, cursor int64, condition, response, status string, task domain.Task, run *domain.Run, intent *domain.SchedulingIntent, reasons []string, constraints map[string]any, now string) (domain.SupervisorAction, error) {
	id, err := randomID("saction_")
	if err != nil {
		return domain.SupervisorAction{}, err
	}
	action := domain.SupervisorAction{ID: id, WorkspaceID: policy.WorkspaceID, ProjectID: task.ProjectID, ObjectiveID: task.ObjectiveID,
		TaskID: task.ID, AgentID: task.AssignedAgentID, Condition: condition, Response: response, Status: status,
		EntityRevision: task.Revision, PolicyRevision: policy.Revision, AsOfEventSequence: cursor,
		Reasons: reasons, ConstraintSnapshot: constraints, Revision: 1, CreatedAt: now, UpdatedAt: now,
		CreatedBy: "subsystem:supervisor", UpdatedBy: "subsystem:supervisor"}
	if intent != nil {
		action.IntentID, action.AgentID, action.EntityRevision = intent.ID, intent.AgentID, intent.Revision
	}
	if run != nil {
		action.RunID, action.AgentID, action.EntityRevision = run.ID, run.AgentID, run.Revision
	}
	action.ConditionKey = supervisorConditionKeyForAction(action)
	_, action.ContentSHA256, err = canonicalContent(supervisorActionContent{ConditionKey: action.ConditionKey, Condition: action.Condition, Response: action.Response,
		EntityRevision: action.EntityRevision, PolicyRevision: action.PolicyRevision, AsOfEventSequence: action.AsOfEventSequence,
		Reasons: action.Reasons, Constraints: action.ConstraintSnapshot})
	return action, err
}

func insertSupervisorAction(ctx context.Context, tx *sql.Tx, action domain.SupervisorAction) error {
	reasonsJSON, err := json.Marshal(action.Reasons)
	if err != nil {
		return storageFailure("encode supervisor reasons", err)
	}
	constraintsJSON, err := json.Marshal(action.ConstraintSnapshot)
	if err != nil {
		return storageFailure("encode supervisor constraints", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO supervisor_actions(
id,workspace_id,project_id,objective_id,task_id,run_id,prior_run_id,source_proposal_id,source_action_id,agent_id,intent_id,condition,condition_key,response,status,decision,
entity_revision,policy_revision,as_of_event_sequence,reasons_json,constraint_snapshot_json,content_sha256,approval_id,revision,
created_at,updated_at,applied_at,created_by,updated_by) VALUES (?,?,?,NULLIF(?,''),?,NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),NULLIF(?,''),?,?,?,?,NULLIF(?,''),?,?,?,?,?,?,NULLIF(?,''),1,?,?,NULLIF(?,''),?,?)`,
		action.ID, action.WorkspaceID, action.ProjectID, action.ObjectiveID, action.TaskID, action.RunID, action.PriorRunID, action.SourceProposalID, action.SourceActionID, action.AgentID, action.IntentID,
		action.Condition, action.ConditionKey, action.Response, action.Status, action.Decision, action.EntityRevision, action.PolicyRevision,
		action.AsOfEventSequence, string(reasonsJSON), string(constraintsJSON), action.ContentSHA256, action.ApprovalID,
		action.CreatedAt, action.UpdatedAt, action.AppliedAt, action.CreatedBy, action.UpdatedBy)
	if err != nil {
		return storageFailure("insert supervisor action", err)
	}
	return nil
}

func (s *Store) sealSupervisorAction(ctx context.Context, tx *sql.Tx, action domain.SupervisorAction, eventSequence int64) error {
	if !s.supervisorActionSealActive.CompareAndSwap(false, true) {
		return storageFailure("seal supervisor action recording", errors.New("supervisor action receipt construction is already active"))
	}
	defer s.supervisorActionSealActive.Store(false)
	if _, err := tx.ExecContext(ctx, `INSERT INTO supervisor_action_receipts(action_id,workspace_id,condition_key,event_sequence,recorded_status,recorded_at) VALUES (?,?,?,?,?,?)`,
		action.ID, action.WorkspaceID, action.ConditionKey, eventSequence, action.Status, action.CreatedAt); err != nil {
		return storageFailure("seal supervisor action recording", err)
	}
	return nil
}

func canonicalProposalKinds(values []string) ([]string, error) {
	result, err := canonicalIDs(values, 1, 4, "proposal kinds", CodeInvalidManagerGrant)
	if err != nil {
		return nil, err
	}
	order := map[string]int{domain.ManagerProposalAssignment: 0, domain.ManagerProposalEscalation: 1, domain.ManagerProposalReview: 2, domain.ManagerProposalTaskDecomposition: 3}
	for _, value := range result {
		if _, exists := order[value]; !exists {
			return nil, &Error{Code: CodeInvalidManagerGrant, Message: "manager grant proposal kind is invalid"}
		}
	}
	sort.Slice(result, func(i, j int) bool { return order[result[i]] < order[result[j]] })
	return result, nil
}

func canonicalClaimKinds(values []string) ([]string, error) {
	result, err := canonicalIDs(values, 0, 3, "claim kinds", CodeInvalidManagerGrant)
	if err != nil {
		return nil, err
	}
	for _, value := range result {
		if !domain.ValidClaimKind(value) {
			return nil, &Error{Code: CodeInvalidManagerGrant, Message: "manager grant claim kind is invalid"}
		}
	}
	sort.Strings(result)
	return result, nil
}

func canonicalIDs(values []string, minimum, maximum int, label, code string) ([]string, error) {
	if len(values) < minimum || len(values) > maximum {
		return nil, &Error{Code: code, Message: fmt.Sprintf("%s requires %d to %d values", label, minimum, maximum)}
	}
	result := make([]string, len(values))
	seen := map[string]bool{}
	for index, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return nil, &Error{Code: code, Message: label + " must be non-empty and unique"}
		}
		seen[value] = true
		result[index] = value
	}
	sort.Strings(result)
	return result, nil
}

func validManagerProposalLimits(value domain.ManagerProposalLimits) bool {
	return value.MaxOpenProposals >= 1 && value.MaxOpenProposals <= 32 && value.MaxActions >= 1 && value.MaxActions <= 32 &&
		value.MaxTasks >= 1 && value.MaxTasks <= 16 && value.MaxDependencies >= 1 && value.MaxDependencies <= 32 &&
		value.MaxClaimRequirements >= 1 && value.MaxClaimRequirements <= 32 && validBudget(value.Budget)
}

func validDecisionNote(value string) bool { return validManagerText(value, 1024) }
func timestampAfterInstant(value string, instant time.Time) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.After(instant)
}
func validManagerText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}
func validManagerTaskKey(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character != '-' && character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}
func validManagerProposalKind(value string) bool {
	return value == domain.ManagerProposalTaskDecomposition || value == domain.ManagerProposalAssignment || value == domain.ManagerProposalReview || value == domain.ManagerProposalEscalation
}
func validProposalResponse(value string) bool {
	return value == domain.ProposalResponseResumeRun || value == domain.ProposalResponseStopRun || value == domain.ProposalResponseRetryTask || value == domain.ProposalResponseReassignTask
}
func boundedManagementLimit(value int) int {
	if value <= 0 {
		return 100
	}
	if value > 100 {
		return 100
	}
	return value
}
func proposalHasErrors(values []domain.ProposalValidationIssue) bool {
	for _, value := range values {
		if value.Severity == domain.ProposalIssueError {
			return true
		}
	}
	return false
}
func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
func grantProfile(grant domain.ManagerGrant, profileID string) (domain.ManagerGrantLaunchProfile, bool) {
	for _, value := range grant.LaunchProfiles {
		if value.LaunchProfileID == profileID {
			return value, true
		}
	}
	return domain.ManagerGrantLaunchProfile{}, false
}
func addBudgetChecked(left, right domain.Budget) (domain.Budget, bool) {
	if right.TokenLimit > 0 && left.TokenLimit > int64(^uint64(0)>>1)-right.TokenLimit ||
		right.CostCents > 0 && left.CostCents > int64(^uint64(0)>>1)-right.CostCents ||
		right.TimeSeconds > 0 && left.TimeSeconds > int64(^uint64(0)>>1)-right.TimeSeconds {
		return domain.Budget{}, true
	}
	return domain.Budget{TokenLimit: left.TokenLimit + right.TokenLimit, CostCents: left.CostCents + right.CostCents, TimeSeconds: left.TimeSeconds + right.TimeSeconds}, false
}
func budgetExceeds(value, limit domain.Budget) bool {
	return limit.TokenLimit > 0 && value.TokenLimit > limit.TokenLimit || limit.CostCents > 0 && value.CostCents > limit.CostCents || limit.TimeSeconds > 0 && value.TimeSeconds > limit.TimeSeconds
}
