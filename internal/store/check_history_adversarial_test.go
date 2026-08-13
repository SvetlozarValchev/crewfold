package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestFinishedCheckHistorySurvivesDefinitionAndRequirementRetirement(t *testing.T) {
	fixture := newGrantedCheckAuthorityFixture(t)
	work := fixture.requestAndClaim(t)
	fixture.advance()
	started, err := fixture.storage.MarkCheckStarting(context.Background(), fixture.startCommand(work))
	if err != nil || started.LaunchReceipt == nil {
		t.Fatalf("MarkCheckStarting() = %#v, %v", started, err)
	}
	fixture.advance()
	if _, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, "direct:"+work.Run.ID, "record-history-runtime"); err != nil {
		t.Fatalf("RecordCheckRuntimeBinding() error = %v", err)
	}
	fixture.advance()
	if _, err := fixture.storage.MarkCheckRunning(context.Background(), work.Run.ID, "mark-history-running"); err != nil {
		t.Fatalf("MarkCheckRunning() error = %v", err)
	}
	stdout := []byte("immutable historical output\n")
	artifact, err := fixture.storage.PrepareCheckArtifact(context.Background(), domain.CheckArtifactStdout, stdout, 0)
	if err != nil {
		t.Fatalf("PrepareCheckArtifact() error = %v", err)
	}
	fixture.advance()
	exitCode := 0
	terminal := started.LaunchReceipt.Observation
	terminal.ObservedAt = fixture.now.Format(time.RFC3339Nano)
	finished, err := fixture.storage.FinishCheckRun(context.Background(), FinishCheckRunCommand{
		CheckRunID:          work.Run.ID,
		Outcome:             domain.CheckOutcomePassed,
		ExitCode:            &exitCode,
		TerminalObservation: terminal,
		Artifacts:           []PreparedCheckArtifact{artifact},
		CorrelationID:       "finish-history-check",
	})
	if err != nil || finished.Result == nil {
		t.Fatalf("FinishCheckRun() = %#v, %v", finished, err)
	}

	fixture.advance()
	retiredRequirement, err := fixture.storage.RetireTaskCheckRequirement(context.Background(), RetireTaskCheckRequirementCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		RequirementID:       fixture.requirement.ID,
		ExpectedRevision:    fixture.requirement.Revision,
		Reason:              "historical requirement retired",
		IdempotencyKey:      "retire-finished-history-requirement",
		CorrelationID:       "request-retire-finished-history-requirement",
	})
	if err != nil {
		t.Fatalf("RetireTaskCheckRequirement() error = %v", err)
	}
	fixture.advance()
	retiredDefinition, err := fixture.storage.RetireCheckDefinition(context.Background(), RetireCheckDefinitionCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		CheckDefinitionID:   fixture.definition.ID,
		ExpectedRevision:    fixture.definition.Revision,
		Reason:              "historical definition retired",
		IdempotencyKey:      "retire-finished-history-definition",
		CorrelationID:       "request-retire-finished-history-definition",
	})
	if err != nil {
		t.Fatalf("RetireCheckDefinition() error = %v", err)
	}

	assertHistorical := func(label string, detail domain.CheckRunDetail) {
		t.Helper()
		if detail.Run.ID != work.Run.ID || detail.Result == nil || detail.Result.ID != finished.Result.ID {
			t.Fatalf("%s result identity = %#v, want finished run/result", label, detail)
		}
		if detail.Definition.Status != domain.CheckDefinitionRetired || detail.Definition.Revision != retiredDefinition.Value.Revision || !reflect.DeepEqual(detail.Definition.Arguments, fixture.definition.Arguments) {
			t.Fatalf("%s frozen retired definition = %#v, want argv %#v and revision %d", label, detail.Definition, fixture.definition.Arguments, retiredDefinition.Value.Revision)
		}
		if detail.Requirement.Status != domain.CheckRequirementRetired || detail.Requirement.Revision != retiredRequirement.Value.Revision || detail.Requirement.ID != fixture.requirement.ID {
			t.Fatalf("%s frozen retired requirement = %#v", label, detail.Requirement)
		}
		if detail.CurrentFreshness == nil || detail.CurrentFreshness.Status != domain.CheckFreshnessFresh || len(detail.Evidence.MechanicalCheck) != 1 {
			t.Fatalf("%s historical freshness/evidence = %#v/%#v", label, detail.CurrentFreshness, detail.Evidence)
		}
	}

	detail, err := fixture.storage.CheckRunDetail(context.Background(), fixture.workspace.ID, work.Run.ID)
	if err != nil {
		t.Fatalf("CheckRunDetail(after retirement) error = %v", err)
	}
	assertHistorical("owner detail", detail)

	logs, err := fixture.storage.CheckRunLogs(context.Background(), fixture.workspace.ID, work.Run.ID)
	if err != nil {
		t.Fatalf("CheckRunLogs(after retirement) error = %v", err)
	}
	if logs.Stdout == nil || logs.Stdout.Content != string(stdout) || logs.Stdout.ContentSHA256 != artifact.ContentSHA256 {
		t.Fatalf("CheckRunLogs(after retirement) = %#v, want immutable stdout", logs)
	}

	inspected, err := fixture.storage.InspectGrantedCheckResult(context.Background(), fixture.sourceRun.ID, work.Run.ID)
	if err != nil {
		t.Fatalf("InspectGrantedCheckResult(after retirement) error = %v", err)
	}
	assertHistorical("granted inspection", inspected)

	active, err := fixture.storage.TaskCheckRequirements(context.Background(), ListTaskCheckRequirementsQuery{
		WorkspaceIdentifier: fixture.workspace.ID,
		ProjectIdentifier:   fixture.project.ID,
		TaskID:              fixture.task.Task.ID,
		Status:              domain.CheckRequirementActive,
	})
	if err != nil {
		t.Fatalf("TaskCheckRequirements(active after retirement) error = %v", err)
	}
	for _, view := range active {
		if view.Requirement.ID == fixture.requirement.ID {
			t.Fatalf("retired historical requirement remained active-list eligible: %#v", view)
		}
	}
}
