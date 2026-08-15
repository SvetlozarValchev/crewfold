package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

const maximumWorkbenchRetryBytes = 4 * 1024

type workbenchRunRetryRequest struct {
	Workspace            string `json:"workspace"`
	Run                  string `json:"run"`
	ExpectedRunRevision  int64  `json:"expected_run_revision"`
	ExpectedTaskRevision int64  `json:"expected_task_revision"`
	IdempotencyKey       string `json:"idempotency_key"`
}

func (w *workbenchServer) handleWorkbenchRunRetry(response http.ResponseWriter, request *http.Request, session workbenchSession) {
	if !w.authorizeMutation(response, request, session, "workbench run retry") {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maximumWorkbenchRetryBytes))
	if err != nil || rejectDuplicateJSONFields(body) != nil {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "run retry is not exact JSON")
		return
	}
	var params workbenchRunRetryRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&params); err != nil || decodeHasTrailingValue(decoder) || strings.TrimSpace(params.Workspace) == "" || !validCanonicalEntityID(params.Run, "run_") || params.ExpectedRunRevision < 1 || params.ExpectedTaskRevision < 1 || !validWorkbenchText(params.IdempotencyKey, 128) {
		w.writeError(response, http.StatusBadRequest, "invalid_request", "run retry requires exact workspace, run and task revisions, and idempotency key")
		return
	}
	prior, err := w.daemon.store.RunDetail(request.Context(), params.Workspace, params.Run)
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	if prior.Run.Revision != params.ExpectedRunRevision || prior.Task.Revision != params.ExpectedTaskRevision {
		w.writeStoreError(response, &store.Error{Code: store.CodeRunConflict, Message: "run or task revision changed before retry"})
		return
	}
	if prior.Run.Status != domain.RunStartFailed && (prior.Run.Status != domain.RunReview || prior.Task.Status != domain.TaskChangesRequested) {
		w.writeStoreError(response, &store.Error{Code: store.CodeRunConflict, Message: "only an exact start_failed run or review with requested changes can be retried from the workbench"})
		return
	}
	runtimeDriver, exists := w.daemon.runtimes[prior.Run.Runtime]
	if !exists {
		w.writeStoreError(response, &store.Error{Code: store.CodeAdapterUnavailable, Message: "failed run runtime is no longer registered"})
		return
	}
	if err := preflightWorkbenchRuntime(request.Context(), prior.Run.Runtime, runtimeDriver); err != nil {
		w.writeStoreError(response, err)
		return
	}
	provider, exists := w.daemon.providers[prior.Run.Provider]
	if !exists {
		w.writeStoreError(response, &store.Error{Code: store.CodeAdapterUnavailable, Message: "failed run provider is no longer registered"})
		return
	}
	probeContext, cancel := context.WithTimeout(request.Context(), 15*time.Second)
	_, probeErr := diagnoseWorkbenchProvider(probeContext, provider)
	cancel()
	if probeErr != nil {
		w.writeStoreError(response, &store.Error{Code: store.CodeAdapterUnavailable, Message: probeErr.Error()})
		return
	}
	task, err := w.daemon.store.TaskDetail(request.Context(), params.Workspace, prior.Run.TaskID)
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	digest := sha256.Sum256([]byte(params.IdempotencyKey))
	correlationID := "web-retry-" + hex.EncodeToString(digest[:12])
	var result store.RunMutationResult
	if prior.Run.Status == domain.RunReview {
		result, err = w.daemon.store.RetryReviewedRun(request.Context(), store.RetryReviewedRunCommand{
			WorkspaceIdentifier: params.Workspace, PriorRunID: prior.Run.ID,
			ExpectedRunRevision: params.ExpectedRunRevision, ExpectedTaskRevision: params.ExpectedTaskRevision,
			Scenario: ownerWorkbenchScenario(), IdempotencyKey: params.IdempotencyKey, CorrelationID: correlationID,
		})
	} else {
		result, err = w.daemon.store.CreateRun(request.Context(), store.CreateRunCommand{
			WorkspaceIdentifier: params.Workspace, TaskID: prior.Run.TaskID, CheckoutIdentifier: prior.Run.CheckoutID,
			Runtime: prior.Run.Runtime, Provider: prior.Run.Provider, Scenario: ownerWorkbenchScenario(), ExpectedTaskRevision: task.Task.Revision,
			IdempotencyKey: params.IdempotencyKey, CorrelationID: correlationID,
		})
	}
	if err != nil {
		w.writeStoreError(response, err)
		return
	}
	w.writeJSON(response, http.StatusOK, localapi.RunMutationResult{Schema: localapi.RunMutationSchema, Type: "run_mutation", Detail: result.Detail, EventSequence: result.EventSequence})
}
