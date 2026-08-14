package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store"
)

const maximumOwnerIntentBytes = 8 * 1024

type workbenchIntentRequest struct {
	Workspace      string `json:"workspace"`
	Project        string `json:"project"`
	ConversationID string `json:"conversation_id,omitempty"`
	Instruction    string `json:"instruction"`
	Mode           string `json:"mode"`
	IdempotencyKey string `json:"idempotency_key"`
}

type workbenchExecuteRequest struct {
	Workspace string `json:"workspace"`
	TurnID    string `json:"turn_id"`
}

type workbenchPlanEditRequest struct {
	Workspace        string        `json:"workspace"`
	TurnID           string        `json:"turn_id"`
	ExpectedRevision int64         `json:"expected_revision"`
	Title            string        `json:"title"`
	Description      string        `json:"description"`
	Priority         int           `json:"priority"`
	Budget           domain.Budget `json:"budget"`
	Agent            string        `json:"agent"`
}

func (w *workbenchServer) handleOwnerConversation(response http.ResponseWriter, request *http.Request, _ workbenchSession) {
	if request.Method != http.MethodGet {
		w.writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "owner conversation reads require GET")
		return
	}
	workspace := strings.TrimSpace(request.URL.Query().Get("workspace"))
	project := strings.TrimSpace(request.URL.Query().Get("project"))
	conversation := strings.TrimSpace(request.URL.Query().Get("conversation"))
	if workspace == "" || project == "" || len(workspace) > 128 || len(project) > 128 || len(conversation) > 128 {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "conversation read requires an exact workspace and project")
		return
	}
	page, err := w.daemon.store.ListOwnerConversation(request.Context(), workspace, project, conversation)
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	w.writeJSON(response, http.StatusOK, map[string]any{
		"schema": "urn:crewfold:schema:web:owner-conversation:v1", "type": "owner_conversation", "conversations": page.Conversations, "turns": page.Turns,
	})
}

func (w *workbenchServer) handleOwnerIntent(response http.ResponseWriter, request *http.Request, session workbenchSession) {
	if !w.authorizeMutation(response, request, session, "owner intent") {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maximumOwnerIntentBytes))
	if err != nil {
		w.writeError(response, http.StatusRequestEntityTooLarge, "request_too_large", "owner intent exceeds the bounded body limit")
		return
	}
	if err := rejectDuplicateJSONFields(body); err != nil {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "owner intent is not exact JSON")
		return
	}
	var params workbenchIntentRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil || decodeHasTrailingValue(decoder) {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "owner intent does not match the current contract")
		return
	}
	detail, err := w.daemon.store.PrepareOwnerTurn(request.Context(), store.PrepareOwnerTurnCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, ConversationID: params.ConversationID,
		Instruction: params.Instruction, Kind: params.Mode, IdempotencyKey: params.IdempotencyKey,
	})
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	if detail.Turn.Kind == "act" && detail.Turn.Status == "executing" {
		if err := w.daemon.preflightOwnerTurn(request.Context(), detail); err != nil {
			w.writeStoreError(response, err)
			return
		}
		detail, err = w.daemon.executeOwnerTurn(request.Context(), detail)
		if err != nil {
			w.writeStoreError(response, err)
			return
		}
	}
	w.writeJSON(response, http.StatusOK, map[string]any{
		"schema": "urn:crewfold:schema:web:owner-intent:v1", "type": "owner_intent", "detail": detail,
	})
}

func (w *workbenchServer) handleOwnerPlanExecution(response http.ResponseWriter, request *http.Request, session workbenchSession) {
	if !w.authorizeMutation(response, request, session, "owner plan execution") {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maximumOwnerIntentBytes))
	if err != nil || rejectDuplicateJSONFields(body) != nil {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "owner plan execution is not exact JSON")
		return
	}
	var params workbenchExecuteRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil || decodeHasTrailingValue(decoder) || strings.TrimSpace(params.Workspace) == "" || !validCanonicalEntityID(params.TurnID, "turn_") {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "owner plan execution requires exact workspace and turn")
		return
	}
	detail, err := w.daemon.store.OwnerTurnDetail(request.Context(), params.Workspace, params.TurnID)
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	if detail.Turn.Status == "completed" {
		w.writeJSON(response, http.StatusOK, map[string]any{"schema": "urn:crewfold:schema:web:owner-intent:v1", "type": "owner_intent", "detail": detail})
		return
	}
	if err := w.daemon.preflightOwnerTurn(request.Context(), detail); err != nil {
		w.writeStoreError(response, err)
		return
	}
	detail, err = w.daemon.store.StartOwnerTurnExecution(request.Context(), params.Workspace, params.TurnID)
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	detail, err = w.daemon.executeOwnerTurn(request.Context(), detail)
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	w.writeJSON(response, http.StatusOK, map[string]any{"schema": "urn:crewfold:schema:web:owner-intent:v1", "type": "owner_intent", "detail": detail})
}

func (w *workbenchServer) handleOwnerPlanEdit(response http.ResponseWriter, request *http.Request, session workbenchSession) {
	if !w.authorizeMutation(response, request, session, "owner plan edit") {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maximumOwnerIntentBytes))
	if err != nil || rejectDuplicateJSONFields(body) != nil {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "owner plan edit is not exact JSON")
		return
	}
	var params workbenchPlanEditRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil || decodeHasTrailingValue(decoder) || strings.TrimSpace(params.Workspace) == "" || !validCanonicalEntityID(params.TurnID, "turn_") || !validCanonicalEntityID(params.Agent, "agent_") {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "owner plan edit requires exact current fields")
		return
	}
	detail, err := w.daemon.store.EditOwnerPlan(request.Context(), store.EditOwnerPlanCommand{
		WorkspaceIdentifier: params.Workspace, TurnID: params.TurnID, ExpectedRevision: params.ExpectedRevision,
		Title: params.Title, Description: params.Description, Priority: params.Priority, Budget: params.Budget, AgentIdentifier: params.Agent,
	})
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	w.writeJSON(response, http.StatusOK, map[string]any{"schema": "urn:crewfold:schema:web:owner-intent:v1", "type": "owner_intent", "detail": detail})
}

func (s *server) preflightOwnerTurn(ctx context.Context, detail domain.OwnerTurnDetail) error {
	workspace, project := detail.Conversation.WorkspaceID, detail.Conversation.ProjectID
	var selected domain.AgentDefinition
	for _, operation := range detail.Operations {
		if operation.Type == "assign_task" {
			identifier := ownerPayloadString(operation.Payload, "agent_id", "")
			if identifier != "" {
				value, err := s.store.Agent(ctx, workspace, identifier)
				if err != nil {
					return err
				}
				selected = value
			}
			break
		}
	}
	if selected.ID == "" {
		value, err := firstEnabledAgent(ctx, s.store, workspace)
		if err != nil {
			return err
		}
		selected = value
	}
	if !selected.Enabled {
		return &store.Error{Code: store.CodeAgentNotFound, Message: "owner turn selected an agent that is no longer enabled"}
	}
	provider, exists := s.providers[selected.Provider]
	if !exists {
		return &store.Error{Code: store.CodeAdapterUnavailable, Message: "selected agent provider is not registered"}
	}
	runtimeDriver, exists := s.runtimes[selected.Runtime]
	if !exists {
		return &store.Error{Code: store.CodeAdapterUnavailable, Message: "selected agent runtime is not registered"}
	}
	if err := preflightWorkbenchRuntime(ctx, selected.Runtime, runtimeDriver); err != nil {
		return err
	}
	probeContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	_, probeErr := diagnoseWorkbenchProvider(probeContext, provider)
	cancel()
	if probeErr != nil {
		return &store.Error{Code: store.CodeAdapterUnavailable, Message: probeErr.Error()}
	}
	inspection, err := s.store.InspectProject(ctx, workspace, project)
	if err != nil {
		return err
	}
	for _, checkout := range inspection.Checkouts {
		if checkout.Availability == domain.CheckoutAvailable {
			return nil
		}
	}
	return &store.Error{Code: store.CodePlacementUnavailable, Message: "owner act requires one available project checkout"}
}

func (s *server) executeOwnerTurn(ctx context.Context, detail domain.OwnerTurnDetail) (domain.OwnerTurnDetail, error) {
	workspace := detail.Conversation.WorkspaceID
	project := detail.Conversation.ProjectID
	correlation := detail.Turn.ID
	var objectiveID, taskID string
	for _, operation := range detail.Operations {
		if operation.Status == "applied" {
			switch operation.ResultEntityType {
			case "objective":
				objectiveID = operation.ResultEntityID
			case "task", "assignment":
				taskID = operation.ResultEntityID
			}
			continue
		}
		key := operation.ID
		switch operation.Type {
		case "create_objective":
			title := ownerPayloadString(operation.Payload, "title", boundedOwnerTitle(detail.Turn.Instruction))
			command := store.CreateObjectiveCommand{WorkspaceIdentifier: workspace, ProjectIdentifier: project, Title: title, Budget: ownerPayloadBudget(operation.Payload), IdempotencyKey: key, CorrelationID: correlation}
			result, err := s.store.CreateObjective(ctx, command)
			if err != nil {
				return detail, err
			}
			objectiveID = result.Value.ID
			detail, err = s.store.RecordOwnerOperation(ctx, store.RecordOwnerOperationCommand{WorkspaceIdentifier: workspace, TurnID: detail.Turn.ID, OperationID: operation.ID, Method: "objective.create", IdempotencyKey: key, Request: command, Response: result, ResultEntityType: "objective", ResultEntityID: result.Value.ID, EventSequence: result.EventSequence})
			if err != nil {
				return detail, err
			}
		case "create_task":
			if objectiveID == "" {
				return detail, &store.Error{Code: store.CodeOwnerTurnConflict, Message: "owner plan lost its objective dependency"}
			}
			command := store.CreateTaskCommand{WorkspaceIdentifier: workspace, ProjectIdentifier: project, ObjectiveID: objectiveID, Title: ownerPayloadString(operation.Payload, "title", boundedOwnerTitle(detail.Turn.Instruction)), Description: ownerPayloadString(operation.Payload, "description", detail.Turn.Instruction), Priority: ownerPayloadInt(operation.Payload, "priority", 500), Budget: ownerPayloadBudget(operation.Payload), IdempotencyKey: key, CorrelationID: correlation}
			result, err := s.store.CreateTask(ctx, command)
			if err != nil {
				return detail, err
			}
			taskID = result.Detail.Task.ID
			detail, err = s.store.RecordOwnerOperation(ctx, store.RecordOwnerOperationCommand{WorkspaceIdentifier: workspace, TurnID: detail.Turn.ID, OperationID: operation.ID, Method: "task.create", IdempotencyKey: key, Request: command, Response: result, ResultEntityType: "task", ResultEntityID: taskID, EventSequence: result.EventSequence})
			if err != nil {
				return detail, err
			}
		case "assign_task":
			if taskID == "" {
				return detail, &store.Error{Code: store.CodeOwnerTurnConflict, Message: "owner plan lost its task dependency"}
			}
			agent, err := ownerSelectedAgent(ctx, s.store, workspace, operation.Payload)
			if err != nil {
				return detail, err
			}
			task, err := s.store.TaskDetail(ctx, workspace, taskID)
			if err != nil {
				return detail, err
			}
			command := store.AssignTaskCommand{WorkspaceIdentifier: workspace, TaskID: taskID, AgentIdentifier: agent.ID, LeaseSeconds: 24 * 60 * 60, ExpectedRevision: task.Task.Revision, IdempotencyKey: key, CorrelationID: correlation}
			result, err := s.store.AssignTask(ctx, command)
			if err != nil {
				return detail, err
			}
			detail, err = s.store.RecordOwnerOperation(ctx, store.RecordOwnerOperationCommand{WorkspaceIdentifier: workspace, TurnID: detail.Turn.ID, OperationID: operation.ID, Method: "task.assign", IdempotencyKey: key, Request: command, Response: result, ResultEntityType: "assignment", ResultEntityID: taskID, EventSequence: result.EventSequence})
			if err != nil {
				return detail, err
			}
		case "start_run":
			if taskID == "" {
				return detail, &store.Error{Code: store.CodeOwnerTurnConflict, Message: "owner plan lost its assigned task dependency"}
			}
			agent, err := ownerSelectedAgent(ctx, s.store, workspace, operation.Payload)
			if err != nil {
				return detail, err
			}
			inspection, err := s.store.InspectProject(ctx, workspace, project)
			if err != nil {
				return detail, err
			}
			checkoutID := ""
			for _, checkout := range inspection.Checkouts {
				if checkout.Availability == domain.CheckoutAvailable {
					checkoutID = checkout.ID
					break
				}
			}
			if checkoutID == "" {
				return detail, &store.Error{Code: store.CodePlacementUnavailable, Message: "owner plan has no available project checkout"}
			}
			task, err := s.store.TaskDetail(ctx, workspace, taskID)
			if err != nil {
				return detail, err
			}
			scenario := ownerWorkbenchScenario()
			command := store.CreateRunCommand{WorkspaceIdentifier: workspace, TaskID: taskID, CheckoutIdentifier: checkoutID, Runtime: agent.Runtime, Provider: agent.Provider, Scenario: scenario, ExpectedTaskRevision: task.Task.Revision, IdempotencyKey: key, CorrelationID: correlation}
			result, err := s.store.CreateRun(ctx, command)
			if err != nil {
				return detail, err
			}
			detail, err = s.store.RecordOwnerOperation(ctx, store.RecordOwnerOperationCommand{WorkspaceIdentifier: workspace, TurnID: detail.Turn.ID, OperationID: operation.ID, Method: "run.start", IdempotencyKey: key, Request: command, Response: result, ResultEntityType: "run", ResultEntityID: result.Detail.Run.ID, EventSequence: result.EventSequence})
			if err != nil {
				return detail, err
			}
		default:
			return detail, &store.Error{Code: store.CodeOwnerTurnConflict, Message: "owner plan contains an unsupported operation"}
		}
	}
	return s.store.FinishOwnerTurn(ctx, workspace, detail.Turn.ID, "Committed the objective, created and assigned its first task, and requested the selected agent launch. The run inspector shows the exact asynchronous execution state and receipts.")
}

func ownerWorkbenchScenario() domain.FakeScenario {
	return domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "owner-workbench", Acceptance: domain.AcceptanceRule{RequiredEvidence: []string{"task checks and diff inspected"}}, Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "Owner-directed work completed", Evidence: []string{"task checks and diff inspected"}, Handoff: "Completed the owner-directed work and reported its exact evidence."}}}
}

func ownerSelectedAgent(ctx context.Context, storage *store.Store, workspace string, payload map[string]any) (domain.AgentDefinition, error) {
	if identifier := ownerPayloadString(payload, "agent_id", ""); identifier != "" {
		agent, err := storage.Agent(ctx, workspace, identifier)
		if err != nil {
			return domain.AgentDefinition{}, err
		}
		if !agent.Enabled {
			return domain.AgentDefinition{}, &store.Error{Code: store.CodeAgentNotFound, Message: "owner plan selected an agent that is no longer enabled"}
		}
		return agent, nil
	}
	return firstEnabledAgent(ctx, storage, workspace)
}

func ownerPayloadString(payload map[string]any, key, fallback string) string {
	value, ok := payload[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func ownerPayloadInt(payload map[string]any, key string, fallback int) int {
	switch value := payload[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	case json.Number:
		parsed, err := value.Int64()
		if err == nil {
			return int(parsed)
		}
	}
	return fallback
}

func ownerPayloadBudget(payload map[string]any) domain.Budget {
	value, ok := payload["budget"].(map[string]any)
	if !ok {
		return domain.Budget{}
	}
	return domain.Budget{TokenLimit: int64(ownerPayloadInt(value, "token_limit", 0)), CostCents: int64(ownerPayloadInt(value, "cost_cents", 0)), TimeSeconds: int64(ownerPayloadInt(value, "time_seconds", 0))}
}

func firstEnabledAgent(ctx context.Context, storage *store.Store, workspace string) (domain.AgentDefinition, error) {
	page, err := storage.ListAgents(ctx, store.ListAgentsQuery{WorkspaceIdentifier: workspace, Limit: store.MaximumReadPageLimit})
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	for _, agent := range page.Agents {
		if agent.Enabled {
			return agent, nil
		}
	}
	return domain.AgentDefinition{}, &store.Error{Code: store.CodeAgentNotFound, Message: "owner plan has no enabled agent"}
}

func boundedOwnerTitle(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 256 {
		return value
	}
	for len(value) > 256 {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return strings.TrimSpace(value)
}

func (w *workbenchServer) writeStoreError(response http.ResponseWriter, err error) {
	code := store.ErrorCode(err)
	status := http.StatusConflict
	if code == "" {
		code = "workbench_failed"
		status = http.StatusInternalServerError
	}
	if strings.HasPrefix(code, "invalid_") || errors.Is(err, context.Canceled) {
		status = http.StatusBadRequest
	}
	w.writeError(response, status, code, err.Error())
}
