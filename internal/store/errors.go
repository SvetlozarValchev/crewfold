package store

import (
	"errors"
	"fmt"
)

const (
	CodeInvalidWorkspace              = "invalid_workspace"
	CodeWorkspaceExists               = "workspace_already_exists"
	CodeWorkspaceNotFound             = "workspace_not_found"
	CodeInvalidProject                = "invalid_project"
	CodeProjectExists                 = "project_already_exists"
	CodeProjectNotFound               = "project_not_found"
	CodeInvalidCheckout               = "invalid_checkout"
	CodeCheckoutExists                = "checkout_already_registered"
	CodeRepositoryChanged             = "repository_identity_changed"
	CodeInvalidAgent                  = "invalid_agent"
	CodeAgentExists                   = "agent_already_exists"
	CodeAgentNotFound                 = "agent_not_found"
	CodeInvalidObjective              = "invalid_objective"
	CodeObjectiveNotFound             = "objective_not_found"
	CodeInvalidTask                   = "invalid_task"
	CodeTaskNotFound                  = "task_not_found"
	CodeRevisionConflict              = "revision_conflict"
	CodeInvalidTransition             = "invalid_transition"
	CodeDependencyExists              = "dependency_already_exists"
	CodeDependencyCycle               = "dependency_cycle"
	CodeTaskNotReady                  = "task_not_ready"
	CodeAssignmentConflict            = "assignment_conflict"
	CodeInvalidRun                    = "invalid_run"
	CodeRunNotFound                   = "run_not_found"
	CodeRunConflict                   = "run_conflict"
	CodePlacementUnavailable          = "placement_unavailable"
	CodeAdapterUnavailable            = "adapter_unavailable"
	CodeRuntimeFailed                 = "runtime_failed"
	CodeInvalidContext                = "invalid_context"
	CodeContextNotFound               = "context_not_found"
	CodeInvalidContextDelta           = "invalid_context_delta"
	CodeContextDeltaNotFound          = "context_delta_not_found"
	CodeContextRebaseRequired         = "context_rebase_required"
	CodeCapabilityExpired             = "capability_expired"
	CodeCapabilityInactive            = "capability_inactive"
	CodeInvalidReport                 = "invalid_report"
	CodeInvalidMessage                = "invalid_message"
	CodeMessageNotFound               = "message_not_found"
	CodeMessageDenied                 = "message_denied"
	CodeInvalidClaim                  = "invalid_claim"
	CodeClaimNotFound                 = "claim_not_found"
	CodeClaimConflict                 = "claim_conflict"
	CodeOverlapNotFound               = "overlap_not_found"
	CodeSchedulingPaused              = "scheduling_paused"
	CodeInvalidClaimScan              = "invalid_claim_scan"
	CodeInvalidMeeting                = "invalid_meeting"
	CodeMeetingNotFound               = "meeting_not_found"
	CodeMeetingConflict               = "meeting_conflict"
	CodeMeetingStale                  = "meeting_input_stale"
	CodeInvalidKnowledge              = "invalid_knowledge"
	CodeKnowledgeNotFound             = "knowledge_not_found"
	CodeKnowledgeConflict             = "knowledge_conflict"
	CodeKnowledgeDenied               = "knowledge_denied"
	CodeKnowledgeExportPathExists     = "knowledge_export_path_exists"
	CodeInvalidKnowledgeBundlePath    = "invalid_knowledge_bundle_path"
	CodeInvalidKnowledgeBundle        = "invalid_knowledge_bundle"
	CodeKnowledgeBundleDigestMismatch = "knowledge_bundle_digest_mismatch"
	CodeKnowledgeImportScopeConflict  = "knowledge_import_scope_conflict"
	CodeKnowledgeImportConflict       = "knowledge_import_conflict"
	CodeKnowledgeImportDenied         = "knowledge_import_denied"
	CodeContradictionNotFound         = "contradiction_not_found"
	CodeContradictionConflict         = "contradiction_conflict"
	CodeContradictionDenied           = "contradiction_denied"
	CodeInvalidManagerGrant           = "invalid_manager_grant"
	CodeManagerGrantNotFound          = "manager_grant_not_found"
	CodeManagerGrantDenied            = "manager_grant_denied"
	CodeInvalidLaunchProfile          = "invalid_launch_profile"
	CodeLaunchProfileNotFound         = "launch_profile_not_found"
	CodeInvalidManagerProposal        = "invalid_manager_proposal"
	CodeManagerProposalNotFound       = "manager_proposal_not_found"
	CodeManagerProposalConflict       = "manager_proposal_conflict"
	CodeManagerProposalDenied         = "manager_proposal_denied"
	CodeInvalidSupervisorPolicy       = "invalid_supervisor_policy"
	CodeUnsupportedSupervisorEvent    = "unsupported_supervisor_event"
	CodeSupervisorActionNotFound      = "supervisor_action_not_found"
	CodeApprovalNotFound              = "approval_not_found"
	CodeApprovalConflict              = "approval_conflict"
	CodeInvalidCheckDefinition        = "invalid_check_definition"
	CodeCheckDefinitionNotFound       = "check_definition_not_found"
	CodeInvalidCheckRequirement       = "invalid_check_requirement"
	CodeCheckRequirementNotFound      = "check_requirement_not_found"
	CodeCheckRequirementConflict      = "check_requirement_conflict"
	CodeInvalidCheckWatchGrant        = "invalid_check_watch_grant"
	CodeCheckWatchGrantNotFound       = "check_watch_grant_not_found"
	CodeCheckWatchGrantDenied         = "check_watch_grant_denied"
	CodeInvalidCheckRoute             = "invalid_check_route"
	CodeInvalidCheckPolicy            = "invalid_check_policy"
	CodeCheckRunNotFound              = "check_run_not_found"
	CodeCheckRunConflict              = "check_run_conflict"
	CodeCheckCapacityDeferred         = "check_capacity_deferred"
	CodeCheckRuntimeUnknown           = "check_runtime_unknown"
	CodeCheckArtifactUnavailable      = "check_artifact_unavailable"
	CodeUnsupportedCheckEvent         = "unsupported_check_event"
	CodeCheckRepairNotFound           = "check_repair_not_found"
	CodeCheckRepairConflict           = "check_repair_conflict"
	CodeCheckRepairDenied             = "check_repair_denied"
	CodeInvalidOutcomeCommitment      = "invalid_outcome_commitment"
	CodeOutcomeCommitmentNotFound     = "outcome_commitment_not_found"
	CodeOutcomeCommitmentConflict     = "outcome_commitment_conflict"
	CodeInvalidOutcomeAssessment      = "invalid_outcome_assessment"
	CodeOutcomeAssessmentNotFound     = "outcome_assessment_not_found"
	CodeOutcomeAssessmentConflict     = "outcome_assessment_conflict"
	CodeInvalidOwnerCheckpoint        = "invalid_owner_checkpoint"
	CodeOwnerCheckpointNotFound       = "owner_checkpoint_not_found"
	CodeInvalidManagementBriefing     = "invalid_management_briefing"
	CodeManagementBriefingNotFound    = "management_briefing_not_found"
	CodeBriefingClaimNotFound         = "briefing_claim_not_found"
	CodeUnsupportedOutcomeEvent       = "unsupported_outcome_event"
	CodeUnsupportedOperatorEvent      = "unsupported_operator_event"
	CodeInvalidCursor                 = "invalid_cursor"
	CodeRetrievalDegraded             = "retrieval_degraded"
	CodeIdempotencyConflict           = "idempotency_conflict"
	CodeStorageFailed                 = "storage_failed"
)

// Error is a stable domain/storage error that can cross the local API boundary.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *Error) Unwrap() error {
	return e.Cause
}

func ErrorCode(err error) string {
	var storeError *Error
	if errors.As(err, &storeError) {
		return storeError.Code
	}
	return CodeStorageFailed
}
