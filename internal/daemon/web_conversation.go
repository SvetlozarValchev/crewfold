package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	Workspace        string                 `json:"workspace"`
	TurnID           string                 `json:"turn_id"`
	ExpectedRevision int64                  `json:"expected_revision"`
	ObjectiveTitle   string                 `json:"objective_title"`
	ObjectiveBudget  domain.Budget          `json:"objective_budget"`
	Tasks            []domain.OwnerPlanTask `json:"tasks"`
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
		"schema": "urn:crewfold:schema:web:owner-conversation:v1", "type": "owner_conversation", "conversations": page.Conversations, "turns": page.Turns, "review": page.Review,
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
	command := store.PrepareOwnerTurnCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, ConversationID: params.ConversationID,
		Instruction: params.Instruction, Kind: params.Mode, IdempotencyKey: params.IdempotencyKey,
	}
	detail, replayed, err := w.daemon.store.OwnerTurnReplay(request.Context(), command)
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	if !replayed {
		snapshot, snapshotErr := w.daemon.store.BuildOwnerInterpretationSnapshot(request.Context(), params.Workspace, params.Project)
		if snapshotErr != nil {
			w.writeStoreError(response, snapshotErr)
			return
		}
		interpreter := w.daemon.ownerInterpreterForProvider(snapshot.Provider)
		if interpreter == nil {
			w.writeStoreError(response, &store.Error{Code: store.CodeAdapterUnavailable, Message: "owner manager interpreter is unavailable"})
			return
		}
		digest := sha256.Sum256([]byte("owner-manager:" + params.IdempotencyKey))
		interpretation, interpretationErr := interpreter.Interpret(request.Context(), execution.OwnerInterpretationRequest{
			OperationID: "run_" + hex.EncodeToString(digest[:16]), Kind: params.Mode, Instruction: params.Instruction,
			Provider: snapshot.Provider, CheckoutPath: snapshot.CheckoutPath, CanonicalContext: snapshot.CanonicalContext, EventCut: snapshot.EventSequence,
		})
		if interpretationErr != nil {
			w.writeStoreError(response, &store.Error{Code: store.CodeAdapterUnavailable, Message: "owner manager could not produce a typed result", Cause: interpretationErr})
			return
		}
		citations, citationErr := store.ResolveOwnerCitations(snapshot, interpretation.CitationRefs)
		if citationErr != nil {
			w.writeStoreError(response, citationErr)
			return
		}
		command.ExpectedEventSequence = snapshot.EventSequence
		command.Interpretation = interpretation
		command.Citations = citations
		detail, err = w.daemon.store.PrepareOwnerTurn(request.Context(), command)
		if err != nil {
			w.writeStoreError(response, err)
			return
		}
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
	if err := decoder.Decode(&params); err != nil || decodeHasTrailingValue(decoder) || strings.TrimSpace(params.Workspace) == "" || !validCanonicalEntityID(params.TurnID, "turn_") {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "owner plan edit requires exact current fields")
		return
	}
	detail, err := w.daemon.store.EditOwnerPlan(request.Context(), store.EditOwnerPlanCommand{
		WorkspaceIdentifier: params.Workspace, TurnID: params.TurnID, ExpectedRevision: params.ExpectedRevision,
		ObjectiveTitle: params.ObjectiveTitle, ObjectiveBudget: params.ObjectiveBudget, Tasks: params.Tasks,
	})
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	w.writeJSON(response, http.StatusOK, map[string]any{"schema": "urn:crewfold:schema:web:owner-intent:v1", "type": "owner_intent", "detail": detail})
}

func (s *server) preflightOwnerTurn(ctx context.Context, detail domain.OwnerTurnDetail) error {
	workspace, project := detail.Conversation.WorkspaceID, detail.Conversation.ProjectID
	profiles := make(map[string]domain.LaunchProfile)
	for _, operation := range detail.Operations {
		if operation.Type != "schedule_task" {
			continue
		}
		profileID := ownerPayloadString(operation.Payload, "launch_profile_id", "")
		profile, err := s.store.LaunchProfile(ctx, workspace, profileID)
		if err != nil {
			return err
		}
		if profile.ProjectID != project || profile.Status != domain.LaunchProfileActive {
			return &store.Error{Code: store.CodeInvalidLaunchProfile, Message: "owner turn selected a launch profile that is no longer active in this project"}
		}
		profiles[profile.ID] = profile
	}
	if len(profiles) == 0 {
		return &store.Error{Code: store.CodeOwnerTurnConflict, Message: "owner turn has no typed scheduling operations"}
	}
	for _, profile := range profiles {
		selected, err := s.store.Agent(ctx, workspace, profile.AgentID)
		if err != nil {
			return err
		}
		if !selected.Enabled || selected.Revision != profile.AgentRevision || selected.Provider != profile.Provider || selected.Runtime != profile.Runtime {
			return &store.Error{Code: store.CodeAgentNotFound, Message: "owner turn launch profile no longer matches its enabled agent revision"}
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
	var objectiveID string
	taskIDs := make(map[string]string)
	for _, operation := range detail.Operations {
		if operation.Status == "applied" {
			switch operation.ResultEntityType {
			case "objective":
				objectiveID = operation.ResultEntityID
			case "task":
				if key := ownerPayloadString(operation.Payload, "task_key", ""); key != "" {
					taskIDs[key] = operation.ResultEntityID
				}
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
			taskKey := ownerPayloadString(operation.Payload, "task_key", "")
			if taskKey == "" {
				return detail, &store.Error{Code: store.CodeOwnerTurnConflict, Message: "owner plan task lost its stable key"}
			}
			command := store.CreateTaskCommand{WorkspaceIdentifier: workspace, ProjectIdentifier: project, ObjectiveID: objectiveID, Title: ownerPayloadString(operation.Payload, "title", boundedOwnerTitle(detail.Turn.Instruction)), Description: ownerPayloadString(operation.Payload, "description", detail.Turn.Instruction), Priority: ownerPayloadInt(operation.Payload, "priority", 500), Budget: ownerPayloadBudget(operation.Payload), IdempotencyKey: key, CorrelationID: correlation}
			result, err := s.store.CreateTask(ctx, command)
			if err != nil {
				return detail, err
			}
			taskIDs[taskKey] = result.Detail.Task.ID
			detail, err = s.store.RecordOwnerOperation(ctx, store.RecordOwnerOperationCommand{WorkspaceIdentifier: workspace, TurnID: detail.Turn.ID, OperationID: operation.ID, Method: "task.create", IdempotencyKey: key, Request: command, Response: result, ResultEntityType: "task", ResultEntityID: result.Detail.Task.ID, EventSequence: result.EventSequence})
			if err != nil {
				return detail, err
			}
		case "add_dependency":
			taskID := taskIDs[ownerPayloadString(operation.Payload, "task_key", "")]
			dependsOnID := taskIDs[ownerPayloadString(operation.Payload, "depends_on_task_key", "")]
			if taskID == "" || dependsOnID == "" {
				return detail, &store.Error{Code: store.CodeOwnerTurnConflict, Message: "owner plan lost a typed task dependency"}
			}
			task, err := s.store.TaskDetail(ctx, workspace, taskID)
			if err != nil {
				return detail, err
			}
			command := store.AddTaskDependencyCommand{WorkspaceIdentifier: workspace, TaskID: taskID, DependsOnTaskID: dependsOnID, ExpectedRevision: task.Task.Revision, IdempotencyKey: key, CorrelationID: correlation}
			result, err := s.store.AddTaskDependency(ctx, command)
			if err != nil {
				return detail, err
			}
			detail, err = s.store.RecordOwnerOperation(ctx, store.RecordOwnerOperationCommand{WorkspaceIdentifier: workspace, TurnID: detail.Turn.ID, OperationID: operation.ID, Method: "task.dependency.add", IdempotencyKey: key, Request: command, Response: result, ResultEntityType: "task_dependency", ResultEntityID: taskID, EventSequence: result.EventSequence})
			if err != nil {
				return detail, err
			}
		case "schedule_task":
			taskID := taskIDs[ownerPayloadString(operation.Payload, "task_key", "")]
			profileID := ownerPayloadString(operation.Payload, "launch_profile_id", "")
			if taskID == "" || profileID == "" {
				return detail, &store.Error{Code: store.CodeOwnerTurnConflict, Message: "owner plan lost a typed scheduling target"}
			}
			command := store.CreateOwnerSchedulingIntentCommand{WorkspaceIdentifier: workspace, TurnID: detail.Turn.ID, OperationID: operation.ID, TaskID: taskID, LaunchProfileID: profileID, CorrelationID: correlation}
			result, err := s.store.CreateOwnerSchedulingIntent(ctx, command)
			if err != nil {
				return detail, err
			}
			detail, err = s.store.RecordOwnerOperation(ctx, store.RecordOwnerOperationCommand{WorkspaceIdentifier: workspace, TurnID: detail.Turn.ID, OperationID: operation.ID, Method: "supervisor.intent.create", IdempotencyKey: key, Request: command, Response: result, ResultEntityType: "scheduling_intent", ResultEntityID: result.Value.ID, EventSequence: result.EventSequence})
			if err != nil {
				return detail, err
			}
		default:
			return detail, &store.Error{Code: store.CodeOwnerTurnConflict, Message: "owner plan contains an unsupported operation"}
		}
	}
	return s.store.FinishOwnerTurn(ctx, workspace, detail.Turn.ID, "Committed the objective and dependency graph, then published each task to the deterministic supervisor through its exact launch profile. Ready work will launch through Herdr; dependent work remains visibly deferred until its prerequisites complete.")
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
