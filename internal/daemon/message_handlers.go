package daemon

import (
	"context"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleMessageSend(request localapi.Request) localapi.Response {
	var params localapi.MessageSendParams
	if err := decodeParams(request.Params, &params); err != nil || params.ArtifactIDs == nil {
		return invalidParamsResponse(request, "message.send requires workspace, recipient_agent, kind, body, artifact_ids, and idempotency_key")
	}
	result, err := s.store.SendMessage(context.Background(), store.SendMessageCommand{
		WorkspaceIdentifier: params.Workspace, RecipientAgent: params.RecipientAgent,
		ThreadID: params.Thread, ProjectIdentifier: params.Project, TaskID: params.Task,
		Kind: params.Kind, Subject: params.Subject, Body: params.Body, ArtifactIDs: params.ArtifactIDs,
		ReplyToMessageID: params.ReplyToMessage, IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	s.signalMessageWakeWorker()
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.MessageSendResult{Schema: localapi.MessageSendSchema, Type: "message_send", Mutation: result.Value, EventSequence: result.EventSequence})
}

func (s *server) handleInboxList(request localapi.Request) localapi.Response {
	var params localapi.InboxListParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Agent) == "" {
		return invalidParamsResponse(request, "inbox.list requires workspace and agent and accepts a limit from 1 to 50")
	}
	agent, err := s.store.Agent(context.Background(), params.Workspace, params.Agent)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	items, err := s.store.Inbox(context.Background(), agent.WorkspaceID, agent.ID, params.Limit)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.InboxListResult{Schema: localapi.InboxListSchema, Type: "inbox", Agent: agent.ID, Items: items})
}

func (s *server) handleThreadCreate(request localapi.Request) localapi.Response {
	var params localapi.ThreadCreateParams
	if err := decodeParams(request.Params, &params); err != nil || params.Participants == nil {
		return invalidParamsResponse(request, "thread.create requires workspace, subject, two to eight participants, and idempotency_key")
	}
	participants := make([]domain.ParticipantBindingInput, len(params.Participants))
	for index, participant := range params.Participants {
		participants[index] = domain.ParticipantBindingInput{AgentIdentifier: participant.Agent, TaskIdentifier: participant.Task}
	}
	result, err := s.store.CreateParticipantThread(context.Background(), store.CreateParticipantThreadCommand{
		WorkspaceIdentifier: params.Workspace,
		Subject:             params.Subject,
		Participants:        participants,
		IdempotencyKey:      params.IdempotencyKey,
		CorrelationID:       request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ParticipantThreadMutationResult{
		Schema: localapi.ParticipantThreadMutationSchema, Type: "participant_thread_mutation",
		Collaboration: result.Collaboration, EventSequence: result.EventSequence,
	})
}

func (s *server) handleThreadInvite(request localapi.Request) localapi.Response {
	var params localapi.ThreadInviteParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "thread.invite requires workspace, thread, participant, expected_participant_revision, and idempotency_key")
	}
	result, err := s.store.InviteThreadParticipant(context.Background(), store.InviteThreadParticipantCommand{
		WorkspaceIdentifier:         params.Workspace,
		ThreadID:                    params.Thread,
		Participant:                 domain.ParticipantBindingInput{AgentIdentifier: params.Participant.Agent, TaskIdentifier: params.Participant.Task},
		ExpectedParticipantRevision: params.ExpectedParticipantRevision,
		IdempotencyKey:              params.IdempotencyKey,
		CorrelationID:               request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ParticipantThreadMutationResult{
		Schema: localapi.ParticipantThreadMutationSchema, Type: "participant_thread_mutation",
		Collaboration: result.Collaboration, EventSequence: result.EventSequence,
	})
}

func (s *server) handleThreadParticipants(request localapi.Request) localapi.Response {
	var params localapi.ThreadQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Thread) == "" {
		return invalidParamsResponse(request, "thread.participants.list requires workspace and thread")
	}
	collaboration, err := s.store.ParticipantThread(context.Background(), params.Workspace, params.Thread)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ParticipantThreadResult{
		Schema: localapi.ParticipantThreadSchema, Type: "participant_thread", Collaboration: collaboration,
	})
}

func (s *server) handleThreadShow(request localapi.Request) localapi.Response {
	var params localapi.ThreadQueryParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Thread) == "" {
		return invalidParamsResponse(request, "thread.show requires workspace and thread")
	}
	detail, err := s.store.Thread(context.Background(), params.Workspace, params.Thread)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ThreadShowResult{Schema: localapi.ThreadShowSchema, Type: "thread", Detail: detail})
}

func (s *server) handleThreadList(request localapi.Request) localapi.Response {
	var params localapi.ThreadListParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || params.Limit < 0 || params.Limit > 50 {
		return invalidParamsResponse(request, "thread.list requires workspace and accepts project plus a limit from 1 to 50")
	}
	threads, err := s.store.ListThreads(context.Background(), params.Workspace, params.Project, params.Limit)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ThreadListResult{Schema: localapi.ThreadListSchema, Type: "thread_list", Threads: threads})
}
