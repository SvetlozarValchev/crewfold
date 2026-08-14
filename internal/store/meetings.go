package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const (
	meetingCreatedEvent   = "meeting.created"
	meetingPositionsEvent = "meeting.positions_collected"
	meetingProposalEvent  = "meeting.resolution_proposed"
	meetingStalledEvent   = "meeting.stalled"
	meetingConcludedEvent = "meeting.concluded"
	meetingTakeoverEvent  = "meeting.human_takeover"
	meetingActionActorID  = "subsystem:meeting"
	meetingActorType      = domain.EventActorSubsystem
	maximumMeetingTimeout = 7 * 24 * time.Hour
)

func (s *Store) CreateMeeting(ctx context.Context, command CreateMeetingCommand) (MeetingMutationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.OverlapID = strings.TrimSpace(command.OverlapID)
	command.FacilitatorAgent = strings.TrimSpace(command.FacilitatorAgent)
	command.Policy = strings.TrimSpace(command.Policy)
	command.ReviewerAgent = strings.TrimSpace(command.ReviewerAgent)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.Policy == "" {
		command.Policy = domain.MeetingPolicyOwnerDecision
	}
	if command.Timeout == 0 {
		command.Timeout = 30 * time.Minute
	}
	participants, err := normalizeIdentifiers(command.ParticipantAgents, 2, 3)
	if command.WorkspaceIdentifier == "" || command.OverlapID == "" || command.FacilitatorAgent == "" || err != nil || !domain.ValidMeetingPolicy(command.Policy) || command.Timeout < time.Second || command.Timeout > maximumMeetingTimeout {
		return MeetingMutationResult{}, &Error{Code: CodeInvalidMeeting, Message: "meeting requires an open overlap, two or three distinct participants, a facilitator, a valid policy, and a timeout from one second to seven days"}
	}
	command.ParticipantAgents = participants
	allowed, err := normalizeActions(command.AllowedActions)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	command.AllowedActions = allowed
	if command.Policy == domain.MeetingPolicyNamedReviewer && command.ReviewerAgent == "" {
		return MeetingMutationResult{}, &Error{Code: CodeInvalidMeeting, Message: "named_reviewer policy requires a reviewer agent"}
	}
	if command.Policy == domain.MeetingPolicyManagerBounded && len(allowed) == 0 {
		return MeetingMutationResult{}, &Error{Code: CodeInvalidMeeting, Message: "manager_bounded policy requires at least one allowed action"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidMeeting); err != nil {
		return MeetingMutationResult{}, err
	}
	requestHash, err := hashCommand("meeting.create", map[string]any{
		"workspace": command.WorkspaceIdentifier, "overlap": command.OverlapID, "participants": command.ParticipantAgents,
		"facilitator": command.FacilitatorAgent, "policy": command.Policy, "reviewer": command.ReviewerAgent,
		"allowed_actions": command.AllowedActions, "timeout": command.Timeout,
	})
	if err != nil {
		return MeetingMutationResult{}, storageFailure("hash meeting creation", err)
	}
	var replay MeetingMutationResult
	if found, err := s.lookupIdempotencyBeforeEffects(ctx, command.IdempotencyKey, "meeting.create", requestHash, &replay); err != nil {
		return MeetingMutationResult{}, err
	} else if found {
		return replay, nil
	}
	if _, err := s.ReconcileExpiredClaims(ctx, command.WorkspaceIdentifier, derivedCorrelationID(command.CorrelationID, "claims")); err != nil {
		return MeetingMutationResult{}, err
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MeetingMutationResult{}, storageFailure("begin meeting creation", err)
	}
	defer tx.Rollback()
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "meeting.create", requestHash, &replay); err != nil {
		return MeetingMutationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	overlap, err := queryOverlap(ctx, tx, workspace.ID, command.OverlapID)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	if overlap.Status != domain.OverlapOpen {
		return MeetingMutationResult{}, &Error{Code: CodeMeetingConflict, Message: "a meeting can only be created from an open overlap"}
	}
	queries := dbgen.New(tx)
	existing, err := queries.FindActiveMeetingForOverlap(ctx, overlap.ID)
	if err == nil {
		return MeetingMutationResult{}, &Error{Code: CodeMeetingConflict, Message: fmt.Sprintf("overlap already has active meeting %s", existing)}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MeetingMutationResult{}, storageFailure("check active overlap meeting", err)
	}

	agents := make([]domain.AgentDefinition, 0, len(participants)+2)
	agentByID := make(map[string]domain.AgentDefinition)
	resolveAgent := func(identifier string) (domain.AgentDefinition, error) {
		agent, err := queryAgent(ctx, tx, workspace.ID, identifier)
		if err != nil {
			return domain.AgentDefinition{}, err
		}
		if !agent.Enabled {
			return domain.AgentDefinition{}, &Error{Code: CodeMeetingConflict, Message: fmt.Sprintf("agent %s is disabled", agent.ID)}
		}
		if _, ok := agentByID[agent.ID]; !ok {
			agentByID[agent.ID] = agent
			agents = append(agents, agent)
		}
		return agent, nil
	}
	participantAgents := make([]domain.AgentDefinition, 0, len(participants))
	for _, identifier := range participants {
		agent, err := resolveAgent(identifier)
		if err != nil {
			return MeetingMutationResult{}, err
		}
		for _, existingAgent := range participantAgents {
			if existingAgent.ID == agent.ID {
				return MeetingMutationResult{}, &Error{Code: CodeInvalidMeeting, Message: "meeting participants must resolve to distinct agents"}
			}
		}
		participantAgents = append(participantAgents, agent)
	}
	facilitator, err := resolveAgent(command.FacilitatorAgent)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	for _, participant := range participantAgents {
		if participant.ID == facilitator.ID {
			return MeetingMutationResult{}, &Error{Code: CodeInvalidMeeting, Message: "facilitator must be independent from the participants"}
		}
	}
	reviewerID := ""
	if command.ReviewerAgent != "" {
		reviewer, err := resolveAgent(command.ReviewerAgent)
		if err != nil {
			return MeetingMutationResult{}, err
		}
		reviewerID = reviewer.ID
		if reviewer.ID == facilitator.ID {
			return MeetingMutationResult{}, &Error{Code: CodeInvalidMeeting, Message: "named reviewer must be independent from the facilitator"}
		}
		for _, participant := range participantAgents {
			if reviewer.ID == participant.ID {
				return MeetingMutationResult{}, &Error{Code: CodeInvalidMeeting, Message: "named reviewer must be independent from the participants"}
			}
		}
	}
	claims := make([]domain.WorkClaim, 0, len(overlap.ClaimIDs))
	for _, claimID := range overlap.ClaimIDs {
		claim, err := queryClaim(ctx, tx, workspace.ID, claimID)
		if err != nil {
			return MeetingMutationResult{}, err
		}
		claims = append(claims, claim)
	}
	tasks := make([]domain.Task, 0, len(overlap.TaskIDs))
	for _, taskID := range overlap.TaskIDs {
		task, err := queryTask(ctx, tx, workspace.ID, taskID)
		if err != nil {
			return MeetingMutationResult{}, err
		}
		tasks = append(tasks, task)
	}
	now := s.nowText()
	var eventSequence int64
	eventSequence, err = queries.MaxEventSequence(ctx)
	if err != nil {
		return MeetingMutationResult{}, storageFailure("freeze meeting event cursor", err)
	}
	frozen := domain.MeetingInput{Overlap: overlap, Claims: claims, Tasks: tasks, Agents: agents, EventSequence: eventSequence, FrozenAt: now}
	frozenJSON, err := json.Marshal(frozen)
	if err != nil {
		return MeetingMutationResult{}, storageFailure("encode frozen meeting input", err)
	}
	frozenHash, err := hashCommand("meeting.input", frozen)
	if err != nil {
		return MeetingMutationResult{}, storageFailure("hash frozen meeting input", err)
	}
	allowedJSON, _ := json.Marshal(allowed)
	meetingID, err := randomID("meet_")
	if err != nil {
		return MeetingMutationResult{}, storageFailure("generate meeting id", err)
	}
	baseTime, _ := time.Parse(time.RFC3339Nano, now)
	meeting := domain.Meeting{
		ID: meetingID, WorkspaceID: workspace.ID, ProjectID: overlap.ProjectID, OverlapID: overlap.ID,
		Agenda:             fmt.Sprintf("Resolve %s overlap at %s between tasks %s and %s", overlap.Kind, overlap.Witness, overlap.TaskIDs[0], overlap.TaskIDs[1]),
		FacilitatorAgentID: facilitator.ID, Policy: command.Policy, ReviewerAgentID: reviewerID, AllowedActions: allowed,
		Status: domain.MeetingGatheringPositions, FrozenInputHash: frozenHash, DeadlineAt: baseTime.Add(command.Timeout).Format(time.RFC3339Nano),
		Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID,
	}
	if err := queries.InsertMeeting(ctx, dbgen.InsertMeetingParams{
		ID: meeting.ID, WorkspaceID: meeting.WorkspaceID, ProjectID: meeting.ProjectID, OverlapID: meeting.OverlapID,
		Agenda: meeting.Agenda, FacilitatorAgentID: meeting.FacilitatorAgentID, Policy: meeting.Policy,
		ReviewerAgentID: optionalStringPointer(meeting.ReviewerAgentID), AllowedActionsJson: string(allowedJSON), Status: meeting.Status,
		FrozenInputJson: string(frozenJSON), FrozenInputHash: meeting.FrozenInputHash, DeadlineAt: meeting.DeadlineAt,
		Revision: meeting.Revision, CreatedAt: now, UpdatedAt: now, CreatedBy: meeting.CreatedBy, UpdatedBy: meeting.UpdatedBy,
	}); err != nil {
		return MeetingMutationResult{}, storageFailure("insert meeting projection", err)
	}
	for index, agent := range participantAgents {
		taskID := ""
		if index < len(tasks) {
			taskID = tasks[index].ID
		}
		if err := queries.InsertMeetingParticipant(ctx, dbgen.InsertMeetingParticipantParams{MeetingID: meeting.ID, AgentID: agent.ID, TaskID: optionalStringPointer(taskID), Ordinal: int64(index), Status: domain.MeetingParticipantPending}); err != nil {
			return MeetingMutationResult{}, storageFailure("insert meeting participant", err)
		}
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return MeetingMutationResult{}, err
	}
	sequence, err := appendEvent(ctx, tx, workspace.ID, "meeting", meeting.ID, meeting.Revision, meetingCreatedEvent, command.CorrelationID, now, map[string]any{"overlap_id": overlap.ID, "participants": participants, "frozen_input_hash": frozenHash, "policy": meeting.Policy})
	if err != nil {
		return MeetingMutationResult{}, err
	}
	detail, err := meetingDetailInTransaction(ctx, tx, meeting.ID, workspace.ID)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	result := MeetingMutationResult{Detail: detail, EventSequence: sequence}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "meeting.create", requestHash, result, now); err != nil {
		return MeetingMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MeetingMutationResult{}, storageFailure("commit meeting creation", err)
	}
	return result, nil
}

func (s *Store) Meeting(ctx context.Context, workspaceIdentifier, meetingID string) (domain.MeetingDetail, error) {
	workspace, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return domain.MeetingDetail{}, err
	}
	return meetingDetail(ctx, s.db, strings.TrimSpace(meetingID), workspace.ID)
}

func (s *Store) RunMeeting(ctx context.Context, command RunMeetingCommand) (MeetingMutationResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.MeetingID = strings.TrimSpace(command.MeetingID)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.MeetingID == "" || command.ExpectedRevision < 1 {
		return MeetingMutationResult{}, &Error{Code: CodeInvalidMeeting, Message: "meeting run requires workspace, meeting id, and expected revision"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidMeeting); err != nil {
		return MeetingMutationResult{}, err
	}
	requestHash, err := hashCommand("meeting.run", map[string]any{"workspace": command.WorkspaceIdentifier, "meeting": command.MeetingID, "expected_revision": command.ExpectedRevision, "fixture": command.Fixture})
	if err != nil {
		return MeetingMutationResult{}, storageFailure("hash meeting run", err)
	}
	var replay MeetingMutationResult
	if found, err := s.lookupIdempotencyBeforeEffects(ctx, command.IdempotencyKey, "meeting.run", requestHash, &replay); err != nil {
		return MeetingMutationResult{}, err
	} else if found {
		return replay, nil
	}
	if _, err := s.ReconcileExpiredClaims(ctx, command.WorkspaceIdentifier, derivedCorrelationID(command.CorrelationID, "claims")); err != nil {
		return MeetingMutationResult{}, err
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MeetingMutationResult{}, storageFailure("begin meeting run", err)
	}
	defer tx.Rollback()
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "meeting.run", requestHash, &replay); err != nil {
		return MeetingMutationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	detail, err := meetingDetailInTransaction(ctx, tx, command.MeetingID, workspace.ID)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	if detail.Meeting.Revision != command.ExpectedRevision {
		return MeetingMutationResult{}, revisionConflict("meeting", detail.Meeting.ID, command.ExpectedRevision, detail.Meeting.Revision)
	}
	if detail.Meeting.Status == domain.MeetingConcluded || detail.Meeting.Status == domain.MeetingCancelled {
		return MeetingMutationResult{}, &Error{Code: CodeMeetingConflict, Message: fmt.Sprintf("meeting is already %s", detail.Meeting.Status)}
	}
	if detail.Meeting.Status == domain.MeetingAwaitingApproval {
		return MeetingMutationResult{}, &Error{Code: CodeMeetingConflict, Message: "meeting is awaiting owner acceptance, not another facilitator run"}
	}
	if detail.Meeting.Status == domain.MeetingStalled && detail.Proposal != nil {
		return MeetingMutationResult{}, &Error{Code: CodeMeetingConflict, Message: "meeting has a stalled proposal and requires human takeover"}
	}
	now := s.nowText()
	deadline, _ := time.Parse(time.RFC3339Nano, detail.Meeting.DeadlineAt)
	if s.clock().UTC().After(deadline) && detail.Proposal == nil {
		return s.finishMeetingMutation(ctx, tx, command.IdempotencyKey, "meeting.run", requestHash, command.CorrelationID, detail, domain.MeetingStalled, "meeting deadline passed before a proposal was recorded", meetingStalledEvent, now)
	}

	participantSet := make(map[string]struct{}, len(detail.Participants))
	for _, participant := range detail.Participants {
		participantSet[participant.AgentID] = struct{}{}
	}
	positionByAgent := make(map[string]domain.MeetingPositionInput, len(command.Fixture.Positions))
	for _, position := range command.Fixture.Positions {
		position.AgentID = strings.TrimSpace(position.AgentID)
		position.Summary = strings.TrimSpace(position.Summary)
		if position.AgentID == "" || position.Summary == "" || len(position.Summary) > 4096 {
			return MeetingMutationResult{}, &Error{Code: CodeInvalidMeeting, Message: "each meeting position requires an agent and a non-empty summary no larger than 4096 characters"}
		}
		agent, err := queryAgent(ctx, tx, workspace.ID, position.AgentID)
		if err != nil {
			return MeetingMutationResult{}, err
		}
		position.AgentID = agent.ID
		if _, ok := participantSet[agent.ID]; !ok {
			return MeetingMutationResult{}, &Error{Code: CodeInvalidMeeting, Message: fmt.Sprintf("agent %s is not a meeting participant", agent.ID)}
		}
		if _, duplicate := positionByAgent[agent.ID]; duplicate {
			return MeetingMutationResult{}, &Error{Code: CodeInvalidMeeting, Message: "fixture contains duplicate participant positions"}
		}
		positionByAgent[agent.ID] = position
	}
	allSubmitted := true
	for _, participant := range detail.Participants {
		position, supplied := positionByAgent[participant.AgentID]
		if supplied {
			if err := recordMeetingContribution(ctx, tx, detail.Meeting.ID, participant.AgentID, "position", position.Summary, position.Evidence, now); err != nil {
				return MeetingMutationResult{}, err
			}
			if err := dbgen.New(tx).UpdateMeetingParticipantStatus(ctx, dbgen.UpdateMeetingParticipantStatusParams{Status: domain.MeetingParticipantSubmitted, MeetingID: detail.Meeting.ID, AgentID: participant.AgentID}); err != nil {
				return MeetingMutationResult{}, storageFailure("mark meeting participant submitted", err)
			}
		} else if participant.Status != domain.MeetingParticipantSubmitted {
			allSubmitted = false
			if err := dbgen.New(tx).UpdateMeetingParticipantStatus(ctx, dbgen.UpdateMeetingParticipantStatusParams{Status: domain.MeetingParticipantMissing, MeetingID: detail.Meeting.ID, AgentID: participant.AgentID}); err != nil {
				return MeetingMutationResult{}, storageFailure("mark missing meeting participant", err)
			}
		}
	}
	if !allSubmitted {
		return s.finishMeetingMutation(ctx, tx, command.IdempotencyKey, "meeting.run", requestHash, command.CorrelationID, detail, domain.MeetingStalled, "one or more participant positions are missing", meetingStalledEvent, now)
	}
	if command.Fixture.PauseAfterPositions || command.Fixture.Proposal == nil {
		return s.finishMeetingMutation(ctx, tx, command.IdempotencyKey, "meeting.run", requestHash, command.CorrelationID, detail, domain.MeetingFacilitatorPending, "", meetingPositionsEvent, now)
	}
	proposal, actions, err := ensureMeetingProposal(ctx, tx, detail, *command.Fixture.Proposal, now)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	status, reason := domain.MeetingAwaitingApproval, ""
	autoApply := false
	switch detail.Meeting.Policy {
	case domain.MeetingPolicyNamedReviewer:
		if command.Fixture.ReviewerApproved == nil {
			status = domain.MeetingAwaitingReviewer
			break
		}
		if detail.Meeting.ReviewerAgentID == "" {
			return MeetingMutationResult{}, &Error{Code: CodeMeetingConflict, Message: "meeting has no configured reviewer"}
		}
		if err := recordMeetingContribution(ctx, tx, detail.Meeting.ID, detail.Meeting.ReviewerAgentID, "review", strings.TrimSpace(command.Fixture.ReviewerNote), nil, now); err != nil {
			return MeetingMutationResult{}, err
		}
		if !*command.Fixture.ReviewerApproved {
			if err := dbgen.New(tx).DecideMeetingProposal(ctx, dbgen.DecideMeetingProposalParams{Status: domain.MeetingProposalRejected, DecidedAt: &now, DecisionNote: optionalStringPointer(strings.TrimSpace(command.Fixture.ReviewerNote)), ID: proposal.ID}); err != nil {
				return MeetingMutationResult{}, storageFailure("reject meeting proposal", err)
			}
			status, reason = domain.MeetingStalled, "named reviewer rejected the proposal"
			break
		}
		autoApply = true
	case domain.MeetingPolicyManagerBounded:
		autoApply = actionsAllowed(actions, detail.Meeting.AllowedActions)
	}
	if autoApply {
		sequence, err := s.applyMeetingActions(ctx, tx, detail, proposal, actions, command.CorrelationID, now)
		if err != nil {
			return MeetingMutationResult{}, err
		}
		policyNote := "authorized by meeting policy"
		if err := dbgen.New(tx).DecideMeetingProposal(ctx, dbgen.DecideMeetingProposalParams{Status: domain.MeetingProposalAccepted, DecidedAt: &now, DecisionNote: &policyNote, ID: proposal.ID}); err != nil {
			return MeetingMutationResult{}, storageFailure("accept meeting proposal", err)
		}
		return s.finishMeetingMutationWithSequence(ctx, tx, command.IdempotencyKey, "meeting.run", requestHash, command.CorrelationID, detail, domain.MeetingConcluded, "", meetingConcludedEvent, now, sequence)
	}
	eventType := meetingProposalEvent
	if status == domain.MeetingStalled {
		eventType = meetingStalledEvent
	}
	return s.finishMeetingMutation(ctx, tx, command.IdempotencyKey, "meeting.run", requestHash, command.CorrelationID, detail, status, reason, eventType, now)
}

func (s *Store) AcceptMeeting(ctx context.Context, command AcceptMeetingCommand) (MeetingMutationResult, error) {
	command.WorkspaceIdentifier, command.MeetingID = strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.MeetingID)
	command.DecisionNote, command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.DecisionNote), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.MeetingID == "" || command.ExpectedRevision < 1 {
		return MeetingMutationResult{}, &Error{Code: CodeInvalidMeeting, Message: "meeting acceptance requires workspace, meeting id, and expected revision"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidMeeting); err != nil {
		return MeetingMutationResult{}, err
	}
	requestHash, err := hashCommand("meeting.accept", map[string]any{"workspace": command.WorkspaceIdentifier, "meeting": command.MeetingID, "expected_revision": command.ExpectedRevision, "decision_note": command.DecisionNote})
	if err != nil {
		return MeetingMutationResult{}, storageFailure("hash meeting acceptance", err)
	}
	var replay MeetingMutationResult
	if found, err := s.lookupIdempotencyBeforeEffects(ctx, command.IdempotencyKey, "meeting.accept", requestHash, &replay); err != nil {
		return MeetingMutationResult{}, err
	} else if found {
		return replay, nil
	}
	if _, err := s.ReconcileExpiredClaims(ctx, command.WorkspaceIdentifier, derivedCorrelationID(command.CorrelationID, "claims")); err != nil {
		return MeetingMutationResult{}, err
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MeetingMutationResult{}, storageFailure("begin meeting acceptance", err)
	}
	defer tx.Rollback()
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "meeting.accept", requestHash, &replay); err != nil {
		return MeetingMutationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	detail, err := meetingDetailInTransaction(ctx, tx, command.MeetingID, workspace.ID)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	if detail.Meeting.Revision != command.ExpectedRevision {
		return MeetingMutationResult{}, revisionConflict("meeting", detail.Meeting.ID, command.ExpectedRevision, detail.Meeting.Revision)
	}
	if detail.Meeting.Status != domain.MeetingAwaitingApproval || detail.Proposal == nil {
		return MeetingMutationResult{}, &Error{Code: CodeMeetingConflict, Message: "meeting is not awaiting owner approval"}
	}
	sequence, err := s.applyMeetingActions(ctx, tx, detail, *detail.Proposal, detail.Actions, command.CorrelationID, s.nowText())
	if err != nil {
		return MeetingMutationResult{}, err
	}
	now := s.nowText()
	if err := dbgen.New(tx).DecideMeetingProposal(ctx, dbgen.DecideMeetingProposalParams{Status: domain.MeetingProposalAccepted, DecidedAt: &now, DecisionNote: optionalStringPointer(command.DecisionNote), ID: detail.Proposal.ID}); err != nil {
		return MeetingMutationResult{}, storageFailure("accept meeting proposal", err)
	}
	return s.finishMeetingMutationWithSequence(ctx, tx, command.IdempotencyKey, "meeting.accept", requestHash, command.CorrelationID, detail, domain.MeetingConcluded, "", meetingConcludedEvent, now, sequence)
}

func (s *Store) TakeoverMeeting(ctx context.Context, command TakeoverMeetingCommand) (MeetingMutationResult, error) {
	command.WorkspaceIdentifier, command.MeetingID = strings.TrimSpace(command.WorkspaceIdentifier), strings.TrimSpace(command.MeetingID)
	command.DecisionNote, command.IdempotencyKey, command.CorrelationID = strings.TrimSpace(command.DecisionNote), strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" || command.MeetingID == "" || command.ExpectedRevision < 1 || strings.TrimSpace(command.Proposal.Summary) == "" {
		return MeetingMutationResult{}, &Error{Code: CodeInvalidMeeting, Message: "meeting takeover requires workspace, meeting id, expected revision, and a proposal"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidMeeting); err != nil {
		return MeetingMutationResult{}, err
	}
	requestHash, err := hashCommand("meeting.takeover", map[string]any{"workspace": command.WorkspaceIdentifier, "meeting": command.MeetingID, "expected_revision": command.ExpectedRevision, "proposal": command.Proposal, "decision_note": command.DecisionNote})
	if err != nil {
		return MeetingMutationResult{}, storageFailure("hash meeting takeover", err)
	}
	var replay MeetingMutationResult
	if found, err := s.lookupIdempotencyBeforeEffects(ctx, command.IdempotencyKey, "meeting.takeover", requestHash, &replay); err != nil {
		return MeetingMutationResult{}, err
	} else if found {
		return replay, nil
	}
	if _, err := s.ReconcileExpiredClaims(ctx, command.WorkspaceIdentifier, derivedCorrelationID(command.CorrelationID, "claims")); err != nil {
		return MeetingMutationResult{}, err
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return MeetingMutationResult{}, storageFailure("begin meeting takeover", err)
	}
	defer tx.Rollback()
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "meeting.takeover", requestHash, &replay); err != nil {
		return MeetingMutationResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	detail, err := meetingDetailInTransaction(ctx, tx, command.MeetingID, workspace.ID)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	if detail.Meeting.Revision != command.ExpectedRevision {
		return MeetingMutationResult{}, revisionConflict("meeting", detail.Meeting.ID, command.ExpectedRevision, detail.Meeting.Revision)
	}
	if detail.Meeting.Status == domain.MeetingConcluded || detail.Meeting.Status == domain.MeetingCancelled {
		return MeetingMutationResult{}, &Error{Code: CodeMeetingConflict, Message: "completed meeting cannot be taken over"}
	}
	proposal, actions, err := ensureMeetingProposal(ctx, tx, detail, command.Proposal, s.nowText())
	if err != nil {
		return MeetingMutationResult{}, err
	}
	now := s.nowText()
	sequence, err := s.applyMeetingActions(ctx, tx, detail, proposal, actions, command.CorrelationID, now)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	if err := dbgen.New(tx).DecideMeetingProposal(ctx, dbgen.DecideMeetingProposalParams{Status: domain.MeetingProposalAccepted, DecidedAt: &now, DecisionNote: optionalStringPointer(command.DecisionNote), ID: proposal.ID}); err != nil {
		return MeetingMutationResult{}, storageFailure("accept takeover proposal", err)
	}
	return s.finishMeetingMutationWithSequence(ctx, tx, command.IdempotencyKey, "meeting.takeover", requestHash, command.CorrelationID, detail, domain.MeetingConcluded, "", meetingTakeoverEvent, now, sequence)
}

func (s *Store) finishMeetingMutation(ctx context.Context, tx *sql.Tx, key, commandName, requestHash, correlationID string, detail domain.MeetingDetail, status, reason, eventType, now string) (MeetingMutationResult, error) {
	return s.finishMeetingMutationWithSequence(ctx, tx, key, commandName, requestHash, correlationID, detail, status, reason, eventType, now, 0)
}

func (s *Store) finishMeetingMutationWithSequence(ctx context.Context, tx *sql.Tx, key, commandName, requestHash, correlationID string, detail domain.MeetingDetail, status, reason, eventType, now string, sequence int64) (MeetingMutationResult, error) {
	revision := detail.Meeting.Revision + 1
	if err := dbgen.New(tx).UpdateMeetingState(ctx, dbgen.UpdateMeetingStateParams{Status: status, StalledReason: optionalStringPointer(reason), Revision: revision, UpdatedAt: now, UpdatedBy: localOwnerActorID, ID: detail.Meeting.ID}); err != nil {
		return MeetingMutationResult{}, storageFailure("advance meeting projection", err)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return MeetingMutationResult{}, err
	}
	eventSequence, err := appendEvent(ctx, tx, detail.Meeting.WorkspaceID, "meeting", detail.Meeting.ID, revision, eventType, correlationID, now, map[string]any{"status": status, "reason": reason})
	if err != nil {
		return MeetingMutationResult{}, err
	}
	if eventSequence > sequence {
		sequence = eventSequence
	}
	updated, err := meetingDetailInTransaction(ctx, tx, detail.Meeting.ID, detail.Meeting.WorkspaceID)
	if err != nil {
		return MeetingMutationResult{}, err
	}
	result := MeetingMutationResult{Detail: updated, EventSequence: sequence}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return MeetingMutationResult{}, err
	}
	if err := recordIdempotency(ctx, tx, key, commandName, requestHash, result, now); err != nil {
		return MeetingMutationResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MeetingMutationResult{}, storageFailure("commit meeting mutation", err)
	}
	return result, nil
}

func ensureMeetingProposal(ctx context.Context, tx *sql.Tx, detail domain.MeetingDetail, input domain.MeetingProposalInput, now string) (domain.MeetingProposal, []domain.MeetingAction, error) {
	input.Summary = strings.TrimSpace(input.Summary)
	if input.Summary == "" || len(input.Summary) > 4096 || len(input.Actions) == 0 || len(input.Actions) > 20 {
		return domain.MeetingProposal{}, nil, &Error{Code: CodeInvalidMeeting, Message: "proposal requires a summary and one to twenty typed actions"}
	}
	for index := range input.Actions {
		input.Actions[index].Type = strings.TrimSpace(input.Actions[index].Type)
		if !domain.ValidMeetingAction(input.Actions[index].Type) || input.Actions[index].Payload == nil {
			return domain.MeetingProposal{}, nil, &Error{Code: CodeInvalidMeeting, Message: "proposal contains an invalid action"}
		}
		if err := validateMeetingActionInput(detail, input.Actions[index]); err != nil {
			return domain.MeetingProposal{}, nil, err
		}
	}
	if detail.Proposal != nil {
		if detail.Proposal.Summary != input.Summary || !actionInputsEqual(detail.Actions, input.Actions) {
			return domain.MeetingProposal{}, nil, &Error{Code: CodeMeetingConflict, Message: "meeting already has a different frozen proposal"}
		}
		return *detail.Proposal, detail.Actions, nil
	}
	proposalID, err := randomID("proposal_")
	if err != nil {
		return domain.MeetingProposal{}, nil, storageFailure("generate proposal id", err)
	}
	proposal := domain.MeetingProposal{ID: proposalID, MeetingID: detail.Meeting.ID, ProposedBy: detail.Meeting.FacilitatorAgentID, Summary: input.Summary, Status: domain.MeetingProposalProposed, Revision: 1, ProposedAt: now}
	queries := dbgen.New(tx)
	if err := queries.InsertMeetingProposal(ctx, dbgen.InsertMeetingProposalParams{ID: proposal.ID, MeetingID: proposal.MeetingID, ProposedBy: proposal.ProposedBy, Summary: proposal.Summary, Status: proposal.Status, ProposedAt: proposal.ProposedAt}); err != nil {
		return domain.MeetingProposal{}, nil, storageFailure("insert meeting proposal", err)
	}
	actions := make([]domain.MeetingAction, 0, len(input.Actions))
	for index, inputAction := range input.Actions {
		actionID, err := randomID("action_")
		if err != nil {
			return domain.MeetingProposal{}, nil, storageFailure("generate meeting action id", err)
		}
		payload, err := json.Marshal(inputAction.Payload)
		if err != nil {
			return domain.MeetingProposal{}, nil, storageFailure("encode meeting action", err)
		}
		action := domain.MeetingAction{ID: actionID, ProposalID: proposal.ID, Ordinal: index, Type: inputAction.Type, Payload: inputAction.Payload, Status: domain.MeetingActionPending}
		if err := queries.InsertMeetingAction(ctx, dbgen.InsertMeetingActionParams{ID: action.ID, ProposalID: action.ProposalID, Ordinal: int64(action.Ordinal), Type: action.Type, PayloadJson: string(payload), Status: action.Status}); err != nil {
			return domain.MeetingProposal{}, nil, storageFailure("insert meeting action", err)
		}
		actions = append(actions, action)
	}
	return proposal, actions, nil
}

func recordMeetingContribution(ctx context.Context, tx *sql.Tx, meetingID, agentID, round, summary string, evidence []string, now string) error {
	summary = strings.TrimSpace(summary)
	if round == "review" && summary == "" {
		summary = "review decision recorded"
	}
	evidence = append([]string(nil), evidence...)
	if evidence == nil {
		evidence = []string{}
	}
	if len(evidence) > 20 {
		return &Error{Code: CodeInvalidMeeting, Message: "meeting contribution accepts at most 20 evidence references"}
	}
	for index := range evidence {
		evidence[index] = strings.TrimSpace(evidence[index])
		if evidence[index] == "" || len(evidence[index]) > 1024 {
			return &Error{Code: CodeInvalidMeeting, Message: "meeting evidence references must contain 1 to 1024 characters"}
		}
	}
	sort.Strings(evidence)
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return storageFailure("encode meeting contribution", err)
	}
	queries := dbgen.New(tx)
	existing, err := queries.GetMeetingContribution(ctx, dbgen.GetMeetingContributionParams{MeetingID: meetingID, AgentID: agentID, Round: round})
	if err == nil {
		if existing.Summary != summary || existing.EvidenceJson != string(evidenceJSON) {
			return &Error{Code: CodeMeetingConflict, Message: fmt.Sprintf("%s contribution from agent %s was already recorded differently", round, agentID)}
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storageFailure("query existing meeting contribution", err)
	}
	id, err := randomID("contrib_")
	if err != nil {
		return storageFailure("generate meeting contribution id", err)
	}
	if err := queries.InsertMeetingContribution(ctx, dbgen.InsertMeetingContributionParams{ID: id, MeetingID: meetingID, AgentID: agentID, Round: round, Summary: summary, EvidenceJson: string(evidenceJSON), SubmittedAt: now}); err != nil {
		return storageFailure("insert meeting contribution", err)
	}
	return nil
}

func normalizeIdentifiers(values []string, minimum, maximum int) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("identifier cannot be empty")
		}
		if _, ok := seen[value]; ok {
			return nil, errors.New("identifiers must be distinct")
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) < minimum || len(result) > maximum {
		return nil, errors.New("identifier count is outside bounds")
	}
	return result, nil
}

func normalizeActions(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !domain.ValidMeetingAction(value) {
			return nil, &Error{Code: CodeInvalidMeeting, Message: fmt.Sprintf("unknown allowed meeting action %q", value)}
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func actionsAllowed(actions []domain.MeetingAction, allowed []string) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, value := range allowed {
		set[value] = struct{}{}
	}
	for _, action := range actions {
		if _, ok := set[action.Type]; !ok {
			return false
		}
	}
	return true
}

func actionInputsEqual(stored []domain.MeetingAction, supplied []domain.MeetingActionInput) bool {
	if len(stored) != len(supplied) {
		return false
	}
	for index := range stored {
		left, _ := json.Marshal(stored[index].Payload)
		right, _ := json.Marshal(supplied[index].Payload)
		if stored[index].Type != supplied[index].Type || string(left) != string(right) {
			return false
		}
	}
	return true
}

func meetingDetail(ctx context.Context, database *sql.DB, meetingID, workspaceID string) (domain.MeetingDetail, error) {
	meeting, frozen, err := queryMeeting(ctx, database, meetingID, workspaceID)
	if err != nil {
		return domain.MeetingDetail{}, err
	}
	return assembleMeetingDetail(ctx, database, meeting, frozen)
}

func meetingDetailInTransaction(ctx context.Context, tx *sql.Tx, meetingID, workspaceID string) (domain.MeetingDetail, error) {
	meeting, frozen, err := queryMeeting(ctx, tx, meetingID, workspaceID)
	if err != nil {
		return domain.MeetingDetail{}, err
	}
	return assembleMeetingDetail(ctx, tx, meeting, frozen)
}

func queryMeeting(ctx context.Context, database dbgen.DBTX, meetingID, workspaceID string) (domain.Meeting, domain.MeetingInput, error) {
	row, err := dbgen.New(database).GetMeeting(ctx, dbgen.GetMeetingParams{ID: meetingID, WorkspaceID: workspaceID})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Meeting{}, domain.MeetingInput{}, &Error{Code: CodeMeetingNotFound, Message: fmt.Sprintf("meeting %q was not found", meetingID)}
	}
	if err != nil {
		return domain.Meeting{}, domain.MeetingInput{}, storageFailure("query meeting", err)
	}
	meeting := domain.Meeting{
		ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, OverlapID: row.OverlapID,
		Agenda: row.Agenda, FacilitatorAgentID: row.FacilitatorAgentID, Policy: row.Policy,
		ReviewerAgentID: stringValue(row.ReviewerAgentID), Status: row.Status, FrozenInputHash: row.FrozenInputHash,
		DeadlineAt: row.DeadlineAt, StalledReason: stringValue(row.StalledReason), Revision: row.Revision,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CreatedBy: row.CreatedBy, UpdatedBy: row.UpdatedBy,
	}
	var frozen domain.MeetingInput
	if err := json.Unmarshal([]byte(row.AllowedActionsJson), &meeting.AllowedActions); err != nil {
		return domain.Meeting{}, domain.MeetingInput{}, storageFailure("decode allowed meeting actions", err)
	}
	if err := json.Unmarshal([]byte(row.FrozenInputJson), &frozen); err != nil {
		return domain.Meeting{}, domain.MeetingInput{}, storageFailure("decode frozen meeting input", err)
	}
	return meeting, frozen, nil
}

func assembleMeetingDetail(ctx context.Context, database dbgen.DBTX, meeting domain.Meeting, frozen domain.MeetingInput) (domain.MeetingDetail, error) {
	queries := dbgen.New(database)
	detail := domain.MeetingDetail{Meeting: meeting, FrozenInput: frozen, Participants: []domain.MeetingParticipant{}, Contributions: []domain.MeetingContribution{}, Actions: []domain.MeetingAction{}}
	participants, err := queries.ListMeetingParticipants(ctx, meeting.ID)
	if err != nil {
		return domain.MeetingDetail{}, storageFailure("list meeting participants", err)
	}
	for _, participant := range participants {
		detail.Participants = append(detail.Participants, domain.MeetingParticipant{MeetingID: participant.MeetingID, AgentID: participant.AgentID, TaskID: stringValue(participant.TaskID), Ordinal: int(participant.Ordinal), Status: participant.Status})
	}
	contributions, err := queries.ListMeetingContributions(ctx, meeting.ID)
	if err != nil {
		return domain.MeetingDetail{}, storageFailure("list meeting contributions", err)
	}
	for _, row := range contributions {
		contribution := domain.MeetingContribution{ID: row.ID, MeetingID: row.MeetingID, AgentID: row.AgentID, Round: row.Round, Summary: row.Summary, SubmittedAt: row.SubmittedAt}
		if err := json.Unmarshal([]byte(row.EvidenceJson), &contribution.Evidence); err != nil {
			return domain.MeetingDetail{}, storageFailure("decode meeting contribution evidence", err)
		}
		detail.Contributions = append(detail.Contributions, contribution)
	}
	proposalRow, err := queries.GetMeetingProposal(ctx, meeting.ID)
	if err == nil {
		proposal := domain.MeetingProposal{ID: proposalRow.ID, MeetingID: proposalRow.MeetingID, ProposedBy: proposalRow.ProposedBy, Summary: proposalRow.Summary, Status: proposalRow.Status, Revision: proposalRow.Revision, ProposedAt: proposalRow.ProposedAt, DecidedAt: stringValue(proposalRow.DecidedAt), DecisionNote: stringValue(proposalRow.DecisionNote)}
		detail.Proposal = &proposal
		actions, err := queries.ListMeetingActions(ctx, proposal.ID)
		if err != nil {
			return domain.MeetingDetail{}, storageFailure("list meeting actions", err)
		}
		for _, row := range actions {
			action := domain.MeetingAction{ID: row.ID, ProposalID: row.ProposalID, Ordinal: int(row.Ordinal), Type: row.Type, Status: row.Status, ResultEntityID: stringValue(row.ResultEntityID), Diagnostic: stringValue(row.Diagnostic), AppliedAt: stringValue(row.AppliedAt)}
			if err := json.Unmarshal([]byte(row.PayloadJson), &action.Payload); err != nil {
				return domain.MeetingDetail{}, storageFailure("decode meeting action payload", err)
			}
			detail.Actions = append(detail.Actions, action)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.MeetingDetail{}, storageFailure("query meeting proposal", err)
	}
	return detail, nil
}

func optionalStringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
