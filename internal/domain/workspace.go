// Package domain defines provider-, transport-, and storage-neutral Crewfold records.
package domain

import (
	"encoding/json"
	"time"
	"unicode/utf8"
)

const (
	EventActorHuman       = "human"
	EventActorAgentRun    = "agent_run"
	EventActorSubsystem   = "subsystem"
	EventActorIntegration = "integration"
)

func ValidEventActorType(value string) bool {
	switch value {
	case EventActorHuman, EventActorAgentRun, EventActorSubsystem, EventActorIntegration:
		return true
	default:
		return false
	}
}

// KnownEventType reports membership in the current schema's single closed
// event union. Consumers must fail closed before advancing beyond a fact that
// their binary cannot classify.
func KnownEventType(value string) bool {
	switch value {
	case
		"workspace.created",
		"project.registered", "repository.registered", "checkout.registered", "checkout.git_observed",
		"agent.created", "agent.updated", "objective.created", "objective.updated",
		"task.created", "task.updated", "task.dependency_added", "task.assigned", "task.assignment_expired",
		"task.started", "task.blocked", "task.readied", "task.cancelled", "task.completion_proposed",
		"task.changes_requested", "task.completed", "task.failed", "task.handoff_recorded", "task.run_stopped",
		"task.reassigned", "task.role_designated", "task.claim_requirement_created",
		"run.requested", "run.starting", "run.started", "run.runtime_observed", "run.progress_reported",
		"run.blocked", "run.completion_proposed", "run.completed", "run.start_failed", "run.failed",
		"run.resumed", "run.stop_requested", "run.stopped", "run.lost", "run.lost_resolved", "run.report_received",
		"run.artifact_published", "run.tool_called", "run.tool_denied",
		"claim.added", "claim.released", "claim.expired", "claim.drift_opened", "claim.drift_resolved",
		"overlap.opened", "overlap.resolved",
		"thread.created", "thread.participant_added", "message.sent", "message.delivered", "message.read",
		"message.acknowledged", "message.wake_succeeded", "message.wake_failed", "message.wake_failed_unknown",
		"meeting.created", "meeting.positions_collected", "meeting.resolution_proposed", "meeting.stalled",
		"meeting.concluded", "meeting.human_takeover",
		"knowledge.proposed", "knowledge.accepted", "knowledge.rejected", "knowledge.marked_stale",
		"knowledge.superseded", "knowledge.acceptance_denied", "knowledge.rejection_denied",
		"knowledge.stale_denied", "knowledge.imported", "knowledge.import_completed",
		"contradiction.detected", "contradiction.confirmed", "contradiction.dismissed",
		"contradiction.resolved", "contradiction.confirm_denied", "contradiction.dismiss_denied",
		"contradiction.imported",
		"curator.rule_configured", "curator.derived", "curator.auto_accepted",
		"context.packet_built", "context_delta.built", "context_delta.acknowledged",
		"context_delta.rebase_required",
		"manager.grant_created", "manager.grant_revoked", "manager.launch_profile_created",
		"manager.launch_profile_retired", "manager.proposal_submitted", "manager.proposal_accepted",
		"manager.proposal_rejected", "manager.proposal_stale",
		"supervisor.policy_configured", "supervisor.intent_created", "supervisor.intent_satisfied",
		"supervisor.intent_failed", "supervisor.intent_cancelled", "supervisor.action_recorded",
		"supervisor.action_applied", "supervisor.scan_completed",
		"approval.requested", "approval.granted", "approval.denied", "approval.consumed", "approval.expired",
		"check.definition_created", "check.definition_retired", "check.requirement_created", "check.requirement_retired",
		"check.grant_created", "check.grant_revoked", "check.policy_configured", "check.route_created", "check.route_retired",
		"check.run_requested", "check.run_starting", "check.run_runtime_observed", "check.run_started", "check.run_finished", "check.result_recorded",
		"check.freshness_observed", "check.freshness_stale",
		"check.notification_queued", "check.notification_unroutable",
		"check.repair_proposed", "check.repair_accepted", "check.repair_rejected", "check.repair_stale",
		"check.watch_completed",
		"outcome.commitment_created", "outcome.assessment_proposed", "outcome.assessment_accepted",
		"outcome.assessment_rejected", "outcome.assessment_superseded", "owner_checkpoint.created":
		return true
	default:
		return false
	}
}

// ValidEvent reports whether an event satisfies the structural public event
// envelope. Consumers that advance a journal cursor must additionally require
// KnownEventType so a future fact can be stored yet remains fail-closed to an
// older binary.
func ValidEvent(event Event) bool {
	if !validEventID(event.EventID, "evt_") || event.Sequence < 1 || !validEventText(event.Type) || event.SchemaVersion < 1 ||
		!validEventTime(event.OccurredAt) || !validEventTime(event.RecordedAt) || !validEventText(event.Actor.ActorID) ||
		!ValidEventActorType(event.Actor.ActorType) || !validEventID(event.WorkspaceID, "ws_") ||
		!validEventText(event.Entity.Type) || !validEventText(event.Entity.ID) || event.Entity.Revision < 1 ||
		!validBoundedEventText(event.CorrelationID, 128) || (event.CausationID != "" && !validBoundedEventText(event.CausationID, 128)) {
		return false
	}
	var object map[string]json.RawMessage
	return len(event.Data) > 0 && utf8.Valid(event.Data) && json.Valid(event.Data) && json.Unmarshal(event.Data, &object) == nil && object != nil
}

func validEventID(value, prefix string) bool {
	if len(value) != len(prefix)+32 || value[:len(prefix)] != prefix {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validEventText(value string) bool {
	return value != "" && utf8.ValidString(value)
}

func validBoundedEventText(value string, maximum int) bool {
	return validEventText(value) && len(value) <= maximum
}

func validEventTime(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

type Workspace struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Revision  int64  `json:"revision"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	CreatedBy string `json:"created_by"`
	UpdatedBy string `json:"updated_by"`
}

type Event struct {
	EventID       string          `json:"event_id"`
	Sequence      int64           `json:"sequence"`
	Type          string          `json:"type"`
	SchemaVersion int             `json:"schema_version"`
	OccurredAt    string          `json:"occurred_at"`
	RecordedAt    string          `json:"recorded_at"`
	Actor         EventActor      `json:"actor"`
	WorkspaceID   string          `json:"workspace_id"`
	Entity        EventEntity     `json:"entity"`
	CorrelationID string          `json:"correlation_id"`
	CausationID   string          `json:"causation_id,omitempty"`
	Data          json.RawMessage `json:"data"`
}

type EventActor struct {
	ActorID   string `json:"actor_id"`
	ActorType string `json:"actor_type"`
}

type EventEntity struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}
