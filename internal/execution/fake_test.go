package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"crewfold/internal/domain"
)

func TestFakeRuntimeLaunchIsIdempotentByOperationID(t *testing.T) {
	t.Parallel()

	runtime := NewFakeRuntime()
	scenario := domain.FakeScenario{Schema: FakeScenarioSchema, Name: "success", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress}}}
	first, err := runtime.Launch(context.Background(), "run_123", domain.RunPlacement{}, LaunchSpec{Scenario: scenario})
	if err != nil {
		t.Fatalf("Launch(first) error = %v", err)
	}
	second, err := runtime.Launch(context.Background(), "run_123", domain.RunPlacement{}, LaunchSpec{Scenario: scenario})
	if err != nil || second != first || runtime.LaunchCount() != 1 {
		t.Fatalf("Launch(replay) = %#v, %v; first=%#v count=%d", second, err, first, runtime.LaunchCount())
	}
}

func TestFakeRuntimeReportsConfiguredStartFailure(t *testing.T) {
	t.Parallel()

	runtime := NewFakeRuntime()
	_, err := runtime.Launch(context.Background(), "run_failed", domain.RunPlacement{}, LaunchSpec{Scenario: domain.FakeScenario{StartFailure: "fixture refused to start"}})
	var startError *StartError
	if !errors.As(err, &startError) || runtime.LaunchCount() != 0 {
		t.Fatalf("Launch() error = %v, count = %d", err, runtime.LaunchCount())
	}
}

func TestFakeProviderEmitsNormalizedStepsAndAcceptanceIsDeterministic(t *testing.T) {
	t.Parallel()

	scenario := domain.FakeScenario{
		Schema:     FakeScenarioSchema,
		Name:       "complete",
		Acceptance: domain.AcceptanceRule{RequiredEvidence: []string{"tests_passed", "handoff_written"}},
		Steps:      []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Evidence: []string{"tests_passed"}, Handoff: "implementation ready for review"}},
	}
	provider := FakeProvider{}
	observation, exists, err := provider.Next(context.Background(), domain.Run{StepCursor: 0}, scenario)
	if err != nil || !exists || observation.Kind != domain.ObservationCompletion {
		t.Fatalf("Next() = %#v, %t, %v", observation, exists, err)
	}
	passed, missing := AcceptancePasses(scenario.Acceptance, observation.Evidence)
	if passed || len(missing) != 1 || missing[0] != "handoff_written" {
		t.Fatalf("AcceptancePasses() = %t, %v", passed, missing)
	}
}

func TestValidateScenarioRejectsUnknownAndUnboundedBehavior(t *testing.T) {
	t.Parallel()

	for name, scenario := range map[string]domain.FakeScenario{
		"schema":             {Schema: "wrong", Name: "test", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress}}},
		"name":               {Schema: FakeScenarioSchema, Name: "Not Valid", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress}}},
		"empty":              {Schema: FakeScenarioSchema, Name: "empty"},
		"kind":               {Schema: FakeScenarioSchema, Name: "kind", Steps: []domain.FakeStep{{Kind: "shell"}}},
		"question":           {Schema: FakeScenarioSchema, Name: "question", Steps: []domain.FakeStep{{Kind: domain.ObservationBlocked}}},
		"mixed start":        {Schema: FakeScenarioSchema, Name: "mixed", StartFailure: "no", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress}}},
		"completion handoff": {Schema: FakeScenarioSchema, Name: "completion", Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done"}}},
		"completion pause":   {Schema: FakeScenarioSchema, Name: "completion-pause", Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done", Handoff: "review", WaitForResume: true}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateScenario(scenario); err == nil {
				t.Fatal("ValidateScenario() error = nil, want rejection")
			}
		})
	}
}

func TestLoadScenarioRejectsUnknownFieldsAndTrailingDocuments(t *testing.T) {
	t.Parallel()

	for name, contents := range map[string]string{
		"unknown field":     `{"schema":"urn:crewfold:schema:fixture:fake-run-scenario:v1","name":"test","acceptance":{"required_evidence":[]},"steps":[{"kind":"progress"}],"unknown":true}`,
		"trailing document": `{"schema":"urn:crewfold:schema:fixture:fake-run-scenario:v1","name":"test","acceptance":{"required_evidence":[]},"steps":[{"kind":"progress"}]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "scenario.json")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("os.WriteFile() error = %v", err)
			}
			if _, err := LoadScenario(path); err == nil {
				t.Fatal("LoadScenario() error = nil, want rejection")
			}
		})
	}
}
