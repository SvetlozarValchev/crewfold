package daemon

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store"
)

func ownerWorkbenchScenario() domain.FakeScenario {
	// Subscription-backed workers submit typed checks and changed paths through
	// crewfold_propose_completion. Those fields are validated at the MCP boundary;
	// published artifact IDs remain optional evidence links rather than impossible
	// aliases for prose acceptance labels.
	return domain.FakeScenario{
		Schema: execution.FakeScenarioSchema,
		Name:   "owner-workbench",
		Steps: []domain.FakeStep{{
			Kind:    domain.ObservationCompletion,
			Message: "Owner-directed work completed",
			Handoff: "Completed the owner-directed work and reported its exact checks and changed paths.",
		}},
	}
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
