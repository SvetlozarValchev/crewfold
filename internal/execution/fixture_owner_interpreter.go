package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"crewfold/internal/domain"
)

// FixtureOwnerInterpreter is the deterministic provider-free implementation
// used only by Crewfold's fixture providers and black-box acceptance. Normal
// Codex workbenches never route through it.
type FixtureOwnerInterpreter struct{}

func (FixtureOwnerInterpreter) Interpret(_ context.Context, request OwnerInterpretationRequest) (domain.OwnerInterpretation, error) {
	if request.Provider != "fixture" && !strings.HasPrefix(request.Provider, "fixture-") {
		return domain.OwnerInterpretation{}, fmt.Errorf("fixture owner interpreter cannot serve provider %q", request.Provider)
	}
	var snapshot struct {
		Tasks          []json.RawMessage `json:"tasks"`
		Runs           []json.RawMessage `json:"runs"`
		LaunchProfiles []struct {
			ID string `json:"id"`
		} `json:"launch_profiles"`
	}
	if err := json.Unmarshal(request.CanonicalContext, &snapshot); err != nil || len(snapshot.LaunchProfiles) == 0 {
		return domain.OwnerInterpretation{}, errors.New("fixture owner interpretation context is invalid")
	}
	if request.Kind == "query" || request.Kind == "review" {
		return domain.OwnerInterpretation{Disposition: "answer", Summary: "Canonical fixture state", Answer: fmt.Sprintf("This project has %d tasks and %d runs at event cut %d.", len(snapshot.Tasks), len(snapshot.Runs), request.EventCut), ObjectiveBudget: domain.Budget{}, Tasks: []domain.OwnerPlanTask{}, Choices: []domain.OwnerChoice{}, CitationRefs: []string{}}, nil
	}
	title := strings.TrimSpace(request.Instruction)
	if len(title) > 160 {
		title = title[:160]
	}
	return domain.OwnerInterpretation{
		Disposition: "ready", Summary: "Prepared one provider-free fixture task.", ObjectiveTitle: title,
		ObjectiveBudget: domain.Budget{}, Choices: []domain.OwnerChoice{}, CitationRefs: []string{},
		Tasks: []domain.OwnerPlanTask{{Key: "implementation", Title: title, Description: request.Instruction, Priority: 500, Budget: domain.Budget{}, LaunchProfileID: snapshot.LaunchProfiles[0].ID, DependsOn: []string{}}},
	}, nil
}
