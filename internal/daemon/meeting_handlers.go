package daemon

import (
	"context"
	"time"

	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleMeetingCreate(request localapi.Request) localapi.Response {
	var params localapi.MeetingCreateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "meeting.create requires workspace, overlap, two or three participants, facilitator, timeout_seconds, and idempotency_key")
	}
	result, err := s.store.CreateMeeting(context.Background(), store.CreateMeetingCommand{
		WorkspaceIdentifier: params.Workspace, OverlapID: params.Overlap, ParticipantAgents: params.Participants,
		FacilitatorAgent: params.Facilitator, Policy: params.Policy, ReviewerAgent: params.Reviewer,
		AllowedActions: params.AllowedActions, Timeout: time.Duration(params.TimeoutSeconds) * time.Second,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return marshalMeetingMutation(request, result)
}

func (s *server) handleMeetingRun(request localapi.Request) localapi.Response {
	var params localapi.MeetingRunParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "meeting.run requires workspace, meeting, expected_revision, fixture, and idempotency_key")
	}
	result, err := s.store.RunMeeting(context.Background(), store.RunMeetingCommand{
		WorkspaceIdentifier: params.Workspace, MeetingID: params.Meeting, ExpectedRevision: params.ExpectedRevision,
		Fixture: params.Fixture, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return marshalMeetingMutation(request, result)
}

func (s *server) handleMeetingInspect(request localapi.Request) localapi.Response {
	var params localapi.MeetingQueryParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "meeting.inspect requires workspace and meeting")
	}
	detail, err := s.store.Meeting(context.Background(), params.Workspace, params.Meeting)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.MeetingInspectResult{Schema: localapi.MeetingInspectSchema, Type: "meeting", Detail: detail})
}

func (s *server) handleMeetingAccept(request localapi.Request) localapi.Response {
	var params localapi.MeetingAcceptParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "meeting.accept requires workspace, meeting, expected_revision, and idempotency_key")
	}
	result, err := s.store.AcceptMeeting(context.Background(), store.AcceptMeetingCommand{
		WorkspaceIdentifier: params.Workspace, MeetingID: params.Meeting, ExpectedRevision: params.ExpectedRevision,
		DecisionNote: params.DecisionNote, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return marshalMeetingMutation(request, result)
}

func (s *server) handleMeetingTakeover(request localapi.Request) localapi.Response {
	var params localapi.MeetingTakeoverParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "meeting.takeover requires workspace, meeting, expected_revision, proposal, and idempotency_key")
	}
	result, err := s.store.TakeoverMeeting(context.Background(), store.TakeoverMeetingCommand{
		WorkspaceIdentifier: params.Workspace, MeetingID: params.Meeting, ExpectedRevision: params.ExpectedRevision,
		Proposal: params.Proposal, DecisionNote: params.DecisionNote, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return marshalMeetingMutation(request, result)
}

func marshalMeetingMutation(request localapi.Request, result store.MeetingMutationResult) localapi.Response {
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.MeetingMutationResult{
		Schema: localapi.MeetingMutationSchema, Type: "meeting_mutation", Detail: result.Detail, EventSequence: result.EventSequence,
	})
}
