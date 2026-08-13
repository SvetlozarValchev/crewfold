package execution

import (
	"strings"
	"testing"

	"crewfold/internal/domain"
)

func TestFixtureCheckWatchValidationClosesTrustedScope(t *testing.T) {
	t.Parallel()
	valid := domain.FixtureCheckWatch{
		RunRequirementID:        "checkreq_0123456789abcdef0123456789abcdef",
		ListResults:             true,
		InspectCheckRunID:       "checkrun_0123456789abcdef0123456789abcdef",
		ProposeRepairResultID:   "checkresult_0123456789abcdef0123456789abcdef",
		RepairRationale:         "The exact nonpass result needs bounded repair work.",
		ProbeReservedAcceptance: true,
	}
	if err := validateFixtureCheckWatch(valid); err != nil {
		t.Fatalf("valid fixture check-watch plan error = %v", err)
	}
	for name, mutate := range map[string]func(*domain.FixtureCheckWatch){
		"wrong requirement prefix":     func(plan *domain.FixtureCheckWatch) { plan.RunRequirementID = "task_0123456789abcdef0123456789abcdef" },
		"wrong check-run prefix":       func(plan *domain.FixtureCheckWatch) { plan.InspectCheckRunID = plan.RunRequirementID },
		"missing rationale":            func(plan *domain.FixtureCheckWatch) { plan.RepairRationale = "" },
		"oversized rationale":          func(plan *domain.FixtureCheckWatch) { plan.RepairRationale = strings.Repeat("x", 4097) },
		"denied mixed with operations": func(plan *domain.FixtureCheckWatch) { plan.ExpectToolsDenied = true },
		"revocation without delay":     func(plan *domain.FixtureCheckWatch) { plan.ProbeRevokedGrant = true },
	} {
		candidate := valid
		mutate(&candidate)
		if err := validateFixtureCheckWatch(candidate); err == nil {
			t.Errorf("%s fixture check-watch plan unexpectedly validated", name)
		}
	}
	if err := validateFixtureCheckWatch(domain.FixtureCheckWatch{ExpectToolsDenied: true}); err != nil {
		t.Fatalf("denied-only fixture check-watch plan error = %v", err)
	}
}

func TestFixtureScenarioRejectsMixedManagerAndCheckWatchAuthority(t *testing.T) {
	t.Parallel()
	scenario := domain.FakeScenario{
		Schema: FakeScenarioSchema, Name: "mixed-authority",
		Steps:      []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "probe immutable authority"}},
		Management: domain.FixtureManagement{ExpectToolsDenied: true},
		CheckWatch: domain.FixtureCheckWatch{ExpectToolsDenied: true},
	}
	if err := ValidateScenario(scenario); err == nil || !strings.Contains(err.Error(), "cannot combine") {
		t.Fatalf("mixed fixture authority validation error = %v", err)
	}
}
