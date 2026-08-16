package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

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
	IdempotencyKey string `json:"idempotency_key"`
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
		"schema": "urn:crewfold:schema:web:owner-conversation:v1", "type": "owner_conversation", "conversations": page.Conversations, "turns": page.Turns, "exchanges": page.Exchanges, "executive": page.Executive, "review": page.Review,
	})
}

func (w *workbenchServer) handleOwnerIntent(response http.ResponseWriter, request *http.Request, session workbenchSession) {
	if !w.authorizeMutation(response, request, session, "owner instruction") {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maximumOwnerIntentBytes))
	if err != nil {
		w.writeError(response, http.StatusRequestEntityTooLarge, "request_too_large", "owner instruction exceeds the bounded body limit")
		return
	}
	if err := rejectDuplicateJSONFields(body); err != nil {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "owner instruction is not exact JSON")
		return
	}
	var params workbenchIntentRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil || decodeHasTrailingValue(decoder) {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "owner instruction does not match the current contract")
		return
	}
	snapshot, err := w.daemon.store.BuildOwnerInterpretationSnapshot(request.Context(), params.Workspace, params.Project)
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	result, err := w.daemon.store.RequestOwnerExecutiveTurn(request.Context(), store.RequestOwnerExecutiveTurnCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, ConversationID: params.ConversationID,
		Instruction: params.Instruction, Kind: "instruction", InitiatedBy: "owner", IdempotencyKey: params.IdempotencyKey, Snapshot: snapshot,
	})
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	w.daemon.signalOwnerExecutiveWorker()
	w.writeJSON(response, http.StatusOK, map[string]any{
		"schema": "urn:crewfold:schema:web:owner-intent:v1", "type": "owner_intent", "detail": result.Detail, "exchange": result.Exchange,
	})
}

func ownerWorkbenchScenario() domain.FakeScenario {
	return domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "owner-workbench", Acceptance: domain.AcceptanceRule{RequiredEvidence: []string{"task checks and diff inspected"}}, Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "Owner-directed work completed", Evidence: []string{"task checks and diff inspected"}, Handoff: "Completed the owner-directed work and reported its exact evidence."}}}
}

func ownerExecutiveScenario() domain.FakeScenario {
	return domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "owner-executive", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "Awaiting one typed owner response.", WaitForResume: true}}}
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
