package store

import (
	"errors"
	"fmt"
)

const (
	CodeInvalidWorkspace     = "invalid_workspace"
	CodeWorkspaceExists      = "workspace_already_exists"
	CodeWorkspaceNotFound    = "workspace_not_found"
	CodeInvalidProject       = "invalid_project"
	CodeProjectExists        = "project_already_exists"
	CodeProjectNotFound      = "project_not_found"
	CodeInvalidCheckout      = "invalid_checkout"
	CodeCheckoutExists       = "checkout_already_registered"
	CodeRepositoryChanged    = "repository_identity_changed"
	CodeInvalidAgent         = "invalid_agent"
	CodeAgentExists          = "agent_already_exists"
	CodeAgentNotFound        = "agent_not_found"
	CodeInvalidObjective     = "invalid_objective"
	CodeObjectiveNotFound    = "objective_not_found"
	CodeInvalidTask          = "invalid_task"
	CodeTaskNotFound         = "task_not_found"
	CodeRevisionConflict     = "revision_conflict"
	CodeInvalidTransition    = "invalid_transition"
	CodeDependencyExists     = "dependency_already_exists"
	CodeDependencyCycle      = "dependency_cycle"
	CodeTaskNotReady         = "task_not_ready"
	CodeAssignmentConflict   = "assignment_conflict"
	CodeInvalidRun           = "invalid_run"
	CodeRunNotFound          = "run_not_found"
	CodeRunConflict          = "run_conflict"
	CodePlacementUnavailable = "placement_unavailable"
	CodeAdapterUnavailable   = "adapter_unavailable"
	CodeRuntimeFailed        = "runtime_failed"
	CodeInvalidContext       = "invalid_context"
	CodeContextNotFound      = "context_not_found"
	CodeCapabilityExpired    = "capability_expired"
	CodeCapabilityInactive   = "capability_inactive"
	CodeInvalidReport        = "invalid_report"
	CodeInvalidMessage       = "invalid_message"
	CodeMessageNotFound      = "message_not_found"
	CodeMessageDenied        = "message_denied"
	CodeInvalidClaim         = "invalid_claim"
	CodeClaimNotFound        = "claim_not_found"
	CodeClaimConflict        = "claim_conflict"
	CodeOverlapNotFound      = "overlap_not_found"
	CodeSchedulingPaused     = "scheduling_paused"
	CodeInvalidClaimScan     = "invalid_claim_scan"
	CodeInvalidMeeting       = "invalid_meeting"
	CodeMeetingNotFound      = "meeting_not_found"
	CodeMeetingConflict      = "meeting_conflict"
	CodeMeetingStale         = "meeting_input_stale"
	CodeIdempotencyConflict  = "idempotency_conflict"
	CodeStorageFailed        = "storage_failed"
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
