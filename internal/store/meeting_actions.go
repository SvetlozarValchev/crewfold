package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

func validateMeetingActionInput(detail domain.MeetingDetail, action domain.MeetingActionInput) error {
	taskAllowed := func(id string) bool {
		for _, task := range detail.FrozenInput.Tasks {
			if task.ID == id {
				return true
			}
		}
		return false
	}
	agentAllowed := func(id string) bool {
		for _, agent := range detail.FrozenInput.Agents {
			if agent.ID == id || agent.Name == id {
				return true
			}
		}
		return false
	}
	requireTask := func(key string) (string, error) {
		value, err := payloadString(action.Payload, key)
		if err != nil || !taskAllowed(value) {
			return "", &Error{Code: CodeInvalidMeeting, Message: fmt.Sprintf("%s action requires frozen meeting task %s", action.Type, key)}
		}
		return value, nil
	}
	switch action.Type {
	case domain.MeetingActionSequence:
		if err := validatePayloadKeys(action.Payload, "before_task_id", "after_task_id"); err != nil {
			return err
		}
		before, err := requireTask("before_task_id")
		if err != nil {
			return err
		}
		after, err := requireTask("after_task_id")
		if err != nil {
			return err
		}
		if before == after {
			return &Error{Code: CodeInvalidMeeting, Message: "sequence action requires two distinct frozen tasks"}
		}
	case domain.MeetingActionSplit:
		if err := validatePayloadKeys(action.Payload, "source_task_id", "title", "description"); err != nil {
			return err
		}
		if _, err := requireTask("source_task_id"); err != nil {
			return err
		}
		title, err := payloadString(action.Payload, "title")
		if err != nil || !validTitle(title) {
			return &Error{Code: CodeInvalidMeeting, Message: "split action requires a valid title"}
		}
		if description, ok := optionalPayloadString(action.Payload, "description"); ok && len(description) > 4096 {
			return &Error{Code: CodeInvalidMeeting, Message: "split task description is too large"}
		}
	case domain.MeetingActionReassign:
		if err := validatePayloadKeys(action.Payload, "task_id", "agent_id", "lease_seconds"); err != nil {
			return err
		}
		if _, err := requireTask("task_id"); err != nil {
			return err
		}
		agent, err := payloadString(action.Payload, "agent_id")
		if err != nil || !agentAllowed(agent) {
			return &Error{Code: CodeInvalidMeeting, Message: "reassign action requires an agent in the frozen meeting"}
		}
		lease, err := payloadInt64(action.Payload, "lease_seconds", 300)
		if err != nil || lease < 1 || lease > 30*24*60*60 {
			return &Error{Code: CodeInvalidMeeting, Message: "reassign action lease_seconds must be from 1 second to 30 days"}
		}
	case domain.MeetingActionDesignateRole:
		if err := validatePayloadKeys(action.Payload, "task_id", "agent_id", "role"); err != nil {
			return err
		}
		if _, err := requireTask("task_id"); err != nil {
			return err
		}
		agent, err := payloadString(action.Payload, "agent_id")
		if err != nil || !agentAllowed(agent) {
			return &Error{Code: CodeInvalidMeeting, Message: "designate_role action requires an agent in the frozen meeting"}
		}
		role, err := payloadString(action.Payload, "role")
		if err != nil || role != "implementer" && role != "reviewer" {
			return &Error{Code: CodeInvalidMeeting, Message: "designate_role action role must be implementer or reviewer"}
		}
	case domain.MeetingActionCancel:
		if err := validatePayloadKeys(action.Payload, "task_id"); err != nil {
			return err
		}
		if _, err := requireTask("task_id"); err != nil {
			return err
		}
	default:
		return &Error{Code: CodeInvalidMeeting, Message: fmt.Sprintf("unknown meeting action %q", action.Type)}
	}
	return nil
}

func validatePayloadKeys(payload map[string]any, allowed ...string) error {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key := range payload {
		if _, ok := set[key]; !ok {
			return &Error{Code: CodeInvalidMeeting, Message: fmt.Sprintf("meeting action payload contains unknown field %q", key)}
		}
	}
	return nil
}

func (s *Store) applyMeetingActions(ctx context.Context, tx *sql.Tx, detail domain.MeetingDetail, proposal domain.MeetingProposal, actions []domain.MeetingAction, correlationID, now string) (int64, error) {
	if err := validateFrozenMeetingInput(ctx, tx, detail); err != nil {
		return 0, err
	}
	var sequence int64
	for _, action := range actions {
		var resultID string
		var eventSequence int64
		var err error
		switch action.Type {
		case domain.MeetingActionSequence:
			resultID, eventSequence, err = s.applySequenceAction(ctx, tx, detail, action, correlationID, now)
		case domain.MeetingActionSplit:
			resultID, eventSequence, err = s.applySplitAction(ctx, tx, detail, action, correlationID, now)
		case domain.MeetingActionReassign:
			resultID, eventSequence, err = s.applyReassignAction(ctx, tx, detail, action, correlationID, now)
		case domain.MeetingActionDesignateRole:
			resultID, eventSequence, err = s.applyDesignationAction(ctx, tx, detail, action, correlationID, now)
		case domain.MeetingActionCancel:
			resultID, eventSequence, err = s.applyCancelAction(ctx, tx, detail, action, correlationID, now)
		default:
			err = &Error{Code: CodeInvalidMeeting, Message: fmt.Sprintf("unknown meeting action %q", action.Type)}
		}
		if err != nil {
			return 0, err
		}
		if eventSequence > sequence {
			sequence = eventSequence
		}
		if err := dbgen.New(tx).MarkMeetingActionApplied(ctx, dbgen.MarkMeetingActionAppliedParams{Status: domain.MeetingActionApplied, ResultEntityID: optionalStringPointer(resultID), AppliedAt: &now, ID: action.ID}); err != nil {
			return 0, storageFailure("mark meeting action applied", err)
		}
	}
	return sequence, nil
}

func validateFrozenMeetingInput(ctx context.Context, tx *sql.Tx, detail domain.MeetingDetail) error {
	overlap, err := queryOverlap(ctx, tx, detail.Meeting.WorkspaceID, detail.FrozenInput.Overlap.ID)
	if err != nil {
		return err
	}
	if overlap.Status != domain.OverlapOpen || overlap.Revision != detail.FrozenInput.Overlap.Revision {
		return &Error{Code: CodeMeetingStale, Message: "meeting overlap changed after its input was frozen"}
	}
	for _, frozen := range detail.FrozenInput.Claims {
		claim, err := queryClaim(ctx, tx, detail.Meeting.WorkspaceID, frozen.ID)
		if err != nil {
			return err
		}
		if claim.Status != domain.ClaimActive || claim.Revision != frozen.Revision {
			return &Error{Code: CodeMeetingStale, Message: fmt.Sprintf("meeting claim %s changed after its input was frozen", frozen.ID)}
		}
	}
	for _, frozen := range detail.FrozenInput.Tasks {
		task, err := queryTask(ctx, tx, detail.Meeting.WorkspaceID, frozen.ID)
		if err != nil {
			return err
		}
		if task.Revision != frozen.Revision {
			return &Error{Code: CodeMeetingStale, Message: fmt.Sprintf("meeting task %s changed after its input was frozen", frozen.ID)}
		}
	}
	return nil
}

func (s *Store) applySequenceAction(ctx context.Context, tx *sql.Tx, detail domain.MeetingDetail, action domain.MeetingAction, correlationID, now string) (string, int64, error) {
	beforeID, _ := payloadString(action.Payload, "before_task_id")
	afterID, _ := payloadString(action.Payload, "after_task_id")
	after, err := queryTask(ctx, tx, detail.Meeting.WorkspaceID, afterID)
	if err != nil {
		return "", 0, err
	}
	if after.Status != domain.TaskReady || after.AssignmentID != "" {
		return "", 0, &Error{Code: CodeMeetingConflict, Message: "sequence can only make a ready unassigned task dependent"}
	}
	queries := dbgen.New(tx)
	existing, err := queries.MeetingDependencyExists(ctx, dbgen.MeetingDependencyExistsParams{TaskID: afterID, DependsOnTaskID: beforeID})
	if err != nil {
		return "", 0, storageFailure("check meeting sequence dependency", err)
	}
	if existing {
		return "", 0, &Error{Code: CodeDependencyExists, Message: "meeting sequence dependency already exists"}
	}
	var cycle int
	err = tx.QueryRowContext(ctx, `
WITH RECURSIVE reachable(id) AS (
    SELECT depends_on_task_id FROM task_dependencies WHERE task_id = ?
    UNION
    SELECT td.depends_on_task_id FROM task_dependencies td JOIN reachable r ON td.task_id = r.id
)
SELECT 1 FROM reachable WHERE id = ? LIMIT 1`, beforeID, afterID).Scan(&cycle)
	if err == nil {
		return "", 0, &Error{Code: CodeDependencyCycle, Message: "meeting sequence would create a dependency cycle"}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", 0, storageFailure("check meeting sequence cycle", err)
	}
	if err := queries.InsertMeetingDependency(ctx, dbgen.InsertMeetingDependencyParams{TaskID: afterID, DependsOnTaskID: beforeID, CreatedAt: now, CreatedBy: detail.Meeting.FacilitatorAgentID}); err != nil {
		return "", 0, storageFailure("insert meeting sequence dependency", err)
	}
	after.Revision++
	if err := queries.TouchMeetingTask(ctx, dbgen.TouchMeetingTaskParams{Revision: after.Revision, UpdatedAt: now, UpdatedBy: detail.Meeting.FacilitatorAgentID, ID: after.ID}); err != nil {
		return "", 0, storageFailure("update sequenced task", err)
	}
	sequence, err := appendEventForActor(ctx, tx, after.WorkspaceID, "task", after.ID, after.Revision, taskDependencyAdded, correlationID, now, meetingActionActorID, meetingActorType, map[string]any{"depends_on_task_id": beforeID, "source_meeting_id": detail.Meeting.ID})
	if err != nil {
		return "", 0, err
	}
	for _, frozenClaim := range detail.FrozenInput.Claims {
		if frozenClaim.TaskID != afterID {
			continue
		}
		claim, err := queryClaim(ctx, tx, detail.Meeting.WorkspaceID, frozenClaim.ID)
		if err != nil {
			return "", 0, err
		}
		claim.Status, claim.Revision = domain.ClaimReleased, claim.Revision+1
		if err := queries.ReleaseMeetingClaim(ctx, dbgen.ReleaseMeetingClaimParams{Status: claim.Status, Revision: claim.Revision, UpdatedAt: now, UpdatedBy: detail.Meeting.FacilitatorAgentID, ID: claim.ID}); err != nil {
			return "", 0, storageFailure("release sequenced task claim", err)
		}
		sequence, err = appendEventForActor(ctx, tx, claim.WorkspaceID, "claim", claim.ID, claim.Revision, claimReleasedEvent, correlationID, now, meetingActionActorID, meetingActorType, map[string]any{"reason": "sequenced behind overlapping task", "source_meeting_id": detail.Meeting.ID})
		if err != nil {
			return "", 0, err
		}
		_, sequence, err = resolveClaimOverlapsForActor(ctx, tx, claim.WorkspaceID, claim.ID, "meeting sequenced overlapping work", correlationID, now, sequence, meetingActionActorID, meetingActorType)
		if err != nil {
			return "", 0, err
		}
	}
	return after.ID, sequence, nil
}

func (s *Store) applySplitAction(ctx context.Context, tx *sql.Tx, detail domain.MeetingDetail, action domain.MeetingAction, correlationID, now string) (string, int64, error) {
	sourceID, _ := payloadString(action.Payload, "source_task_id")
	title, _ := payloadString(action.Payload, "title")
	description, _ := optionalPayloadString(action.Payload, "description")
	source, err := queryTask(ctx, tx, detail.Meeting.WorkspaceID, sourceID)
	if err != nil {
		return "", 0, err
	}
	id, err := randomID("task_")
	if err != nil {
		return "", 0, storageFailure("generate split task id", err)
	}
	if err := dbgen.New(tx).InsertSplitTask(ctx, dbgen.InsertSplitTaskParams{
		ID: id, WorkspaceID: source.WorkspaceID, ProjectID: source.ProjectID, ObjectiveID: optionalStringPointer(source.ObjectiveID),
		Title: strings.TrimSpace(title), Description: optionalStringPointer(strings.TrimSpace(description)), Status: domain.TaskReady,
		Priority: int64(source.Priority), BudgetTokens: source.Budget.TokenLimit, BudgetCostCents: source.Budget.CostCents,
		BudgetTimeSeconds: source.Budget.TimeSeconds, CreatedAt: now, UpdatedAt: now,
		CreatedBy: detail.Meeting.FacilitatorAgentID, UpdatedBy: detail.Meeting.FacilitatorAgentID,
	}); err != nil {
		return "", 0, storageFailure("insert split task", err)
	}
	sequence, err := appendEventForActor(ctx, tx, source.WorkspaceID, "task", id, 1, taskCreated, correlationID, now, meetingActionActorID, meetingActorType, map[string]any{"source_task_id": source.ID, "source_meeting_id": detail.Meeting.ID, "title": strings.TrimSpace(title)})
	return id, sequence, err
}

func (s *Store) applyReassignAction(ctx context.Context, tx *sql.Tx, detail domain.MeetingDetail, action domain.MeetingAction, correlationID, now string) (string, int64, error) {
	taskID, _ := payloadString(action.Payload, "task_id")
	agentIdentifier, _ := payloadString(action.Payload, "agent_id")
	leaseSeconds, _ := payloadInt64(action.Payload, "lease_seconds", 300)
	task, err := queryTask(ctx, tx, detail.Meeting.WorkspaceID, taskID)
	if err != nil {
		return "", 0, err
	}
	if task.Status != domain.TaskReady && task.Status != domain.TaskAssigned {
		return "", 0, &Error{Code: CodeMeetingConflict, Message: "reassign can only change a ready or assigned task"}
	}
	queries := dbgen.New(tx)
	liveRun, err := queries.HasLiveTaskRun(ctx, task.ID)
	if err != nil {
		return "", 0, storageFailure("check live run before reassignment", err)
	}
	if liveRun {
		return "", 0, &Error{Code: CodeMeetingConflict, Message: "task with a live run cannot be reassigned"}
	}
	agent, err := queryAgent(ctx, tx, detail.Meeting.WorkspaceID, agentIdentifier)
	if err != nil {
		return "", 0, err
	}
	if !agent.Enabled {
		return "", 0, &Error{Code: CodeMeetingConflict, Message: "disabled agent cannot receive reassignment"}
	}
	if err := queries.ReleaseTaskAssignmentsForMeeting(ctx, dbgen.ReleaseTaskAssignmentsForMeetingParams{UpdatedAt: now, UpdatedBy: detail.Meeting.FacilitatorAgentID, TaskID: task.ID}); err != nil {
		return "", 0, storageFailure("release previous meeting assignment", err)
	}
	assignmentID, err := randomID("asg_")
	if err != nil {
		return "", 0, storageFailure("generate meeting assignment id", err)
	}
	baseTime, _ := time.Parse(time.RFC3339Nano, now)
	leaseAt := baseTime.Add(time.Duration(leaseSeconds) * time.Second).Format(time.RFC3339Nano)
	if err := queries.InsertMeetingAssignment(ctx, dbgen.InsertMeetingAssignmentParams{ID: assignmentID, TaskID: task.ID, AgentID: agent.ID, LeaseExpiresAt: leaseAt, CreatedAt: now, UpdatedAt: now, CreatedBy: detail.Meeting.FacilitatorAgentID, UpdatedBy: detail.Meeting.FacilitatorAgentID}); err != nil {
		return "", 0, storageFailure("insert meeting assignment", err)
	}
	task.Revision++
	if err := queries.SetMeetingTaskAssigned(ctx, dbgen.SetMeetingTaskAssignedParams{Revision: task.Revision, UpdatedAt: now, UpdatedBy: detail.Meeting.FacilitatorAgentID, ID: task.ID}); err != nil {
		return "", 0, storageFailure("update reassigned task", err)
	}
	sequence, err := appendEventForActor(ctx, tx, task.WorkspaceID, "task", task.ID, task.Revision, "task.reassigned", correlationID, now, meetingActionActorID, meetingActorType, map[string]any{"agent_id": agent.ID, "assignment_id": assignmentID, "source_meeting_id": detail.Meeting.ID})
	return assignmentID, sequence, err
}

func (s *Store) applyDesignationAction(ctx context.Context, tx *sql.Tx, detail domain.MeetingDetail, action domain.MeetingAction, correlationID, now string) (string, int64, error) {
	taskID, _ := payloadString(action.Payload, "task_id")
	agentIdentifier, _ := payloadString(action.Payload, "agent_id")
	role, _ := payloadString(action.Payload, "role")
	task, err := queryTask(ctx, tx, detail.Meeting.WorkspaceID, taskID)
	if err != nil {
		return "", 0, err
	}
	agent, err := queryAgent(ctx, tx, detail.Meeting.WorkspaceID, agentIdentifier)
	if err != nil {
		return "", 0, err
	}
	queries := dbgen.New(tx)
	if err := queries.InsertMeetingTaskRole(ctx, dbgen.InsertMeetingTaskRoleParams{TaskID: task.ID, AgentID: agent.ID, Role: role, SourceMeetingID: detail.Meeting.ID, CreatedAt: now, CreatedBy: detail.Meeting.FacilitatorAgentID}); err != nil {
		return "", 0, storageFailure("insert meeting task role", err)
	}
	task.Revision++
	if err := queries.TouchMeetingTask(ctx, dbgen.TouchMeetingTaskParams{Revision: task.Revision, UpdatedAt: now, UpdatedBy: detail.Meeting.FacilitatorAgentID, ID: task.ID}); err != nil {
		return "", 0, storageFailure("update designated task", err)
	}
	sequence, err := appendEventForActor(ctx, tx, task.WorkspaceID, "task", task.ID, task.Revision, "task.role_designated", correlationID, now, meetingActionActorID, meetingActorType, map[string]any{"agent_id": agent.ID, "role": role, "source_meeting_id": detail.Meeting.ID})
	return task.ID + ":" + agent.ID + ":" + role, sequence, err
}

func (s *Store) applyCancelAction(ctx context.Context, tx *sql.Tx, detail domain.MeetingDetail, action domain.MeetingAction, correlationID, now string) (string, int64, error) {
	taskID, _ := payloadString(action.Payload, "task_id")
	task, err := queryTask(ctx, tx, detail.Meeting.WorkspaceID, taskID)
	if err != nil {
		return "", 0, err
	}
	if task.Status == domain.TaskCompleted || task.Status == domain.TaskCancelled {
		return "", 0, &Error{Code: CodeMeetingConflict, Message: "completed or cancelled task cannot be cancelled by a meeting"}
	}
	queries := dbgen.New(tx)
	liveRun, err := queries.HasLiveTaskRun(ctx, task.ID)
	if err != nil {
		return "", 0, storageFailure("check live run before cancellation", err)
	}
	if liveRun {
		return "", 0, &Error{Code: CodeMeetingConflict, Message: "task with a live run cannot be cancelled"}
	}
	if err := queries.ReleaseTaskAssignmentsForMeeting(ctx, dbgen.ReleaseTaskAssignmentsForMeetingParams{UpdatedAt: now, UpdatedBy: detail.Meeting.FacilitatorAgentID, TaskID: task.ID}); err != nil {
		return "", 0, storageFailure("release cancelled meeting assignment", err)
	}
	task.Revision++
	if err := queries.SetMeetingTaskCancelled(ctx, dbgen.SetMeetingTaskCancelledParams{Revision: task.Revision, UpdatedAt: now, UpdatedBy: detail.Meeting.FacilitatorAgentID, ID: task.ID}); err != nil {
		return "", 0, storageFailure("cancel meeting task", err)
	}
	sequence, err := appendEventForActor(ctx, tx, task.WorkspaceID, "task", task.ID, task.Revision, taskCancelled, correlationID, now, meetingActionActorID, meetingActorType, map[string]any{"source_meeting_id": detail.Meeting.ID})
	if err != nil {
		return "", 0, err
	}
	for _, frozenClaim := range detail.FrozenInput.Claims {
		if frozenClaim.TaskID != task.ID {
			continue
		}
		claim, err := queryClaim(ctx, tx, detail.Meeting.WorkspaceID, frozenClaim.ID)
		if err != nil {
			return "", 0, err
		}
		claim.Status, claim.Revision = domain.ClaimReleased, claim.Revision+1
		if err := queries.ReleaseMeetingClaim(ctx, dbgen.ReleaseMeetingClaimParams{Status: claim.Status, Revision: claim.Revision, UpdatedAt: now, UpdatedBy: detail.Meeting.FacilitatorAgentID, ID: claim.ID}); err != nil {
			return "", 0, storageFailure("release cancelled task claim", err)
		}
		sequence, err = appendEventForActor(ctx, tx, claim.WorkspaceID, "claim", claim.ID, claim.Revision, claimReleasedEvent, correlationID, now, meetingActionActorID, meetingActorType, map[string]any{"reason": "meeting cancelled task", "source_meeting_id": detail.Meeting.ID})
		if err != nil {
			return "", 0, err
		}
		_, sequence, err = resolveClaimOverlapsForActor(ctx, tx, claim.WorkspaceID, claim.ID, "meeting cancelled overlapping work", correlationID, now, sequence, meetingActionActorID, meetingActorType)
		if err != nil {
			return "", 0, err
		}
	}
	return task.ID, sequence, nil
}

func payloadString(payload map[string]any, key string) (string, error) {
	value, ok := payload[key]
	if !ok {
		return "", errors.New("missing payload field")
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", errors.New("payload field must be a non-empty string")
	}
	return strings.TrimSpace(text), nil
}

func optionalPayloadString(payload map[string]any, key string) (string, bool) {
	value, ok := payload[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return strings.TrimSpace(text), ok
}

func payloadInt64(payload map[string]any, key string, defaultValue int64) (int64, error) {
	value, ok := payload[key]
	if !ok {
		return defaultValue, nil
	}
	switch number := value.(type) {
	case float64:
		if number != float64(int64(number)) {
			return 0, errors.New("payload number must be an integer")
		}
		return int64(number), nil
	case int:
		return int64(number), nil
	case int64:
		return number, nil
	case string:
		return strconv.ParseInt(number, 10, 64)
	default:
		return 0, errors.New("payload field must be an integer")
	}
}
